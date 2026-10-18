//go:build windows && !console

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

var lifecycleHandoffEvents = flag.String("sondns-handoff-events", "", "owned account-fixture handle manifest")
var lifecycleHandoffReport = flag.String("sondns-handoff-report", "", "owned account-fixture result file")

type lifecycleHandoffAccessReport struct {
	Before, Granted, Restored string
	RestoreVerified           bool
	Stage                     string
	Failed                    bool
	Error                     string
	NativeError, SessionID    uint32
	EventHandlesVerified      int
}

// This child only changes its own process DACL. It never starts a GUI, service,
// driver, credential prompt or resolver. Only the opted-in VM parent launches it.
func TestLifecycleGUIHandoffAccountChild(t *testing.T) {
	if *lifecycleHandoffEvents == "" {
		t.Skip("owned account process fixture")
	}
	report := lifecycleHandoffAccessReport{Stage: "guard"}
	defer func() {
		if *lifecycleHandoffReport == "" {
			return
		}
		report.Failed = t.Failed()
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(*lifecycleHandoffReport, append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Errorf("write handoff child report: %v", err)
		}
	}()
	check := func(label string, err error) {
		if err != nil {
			report.Error = label + ": " + err.Error()
			var native syscall.Errno
			if errors.As(err, &native) {
				report.NativeError = uint32(native)
			}
			t.Fatal(report.Error)
		}
	}
	if os.Getenv("SONDNS_ACCOUNT_TEST") != "1" || os.Getenv("SONDNS_SERVICE_TEST") != "1" ||
		*lifecycleHandoffReport == "" || windows.GetCurrentProcessToken().IsElevated() {
		report.Error = "handoff child requires the opted-in standard-account fixture"
		t.Fatal(report.Error)
	}
	check("query child session", windows.ProcessIdToSessionId(uint32(os.Getpid()), &report.SessionID))
	report.Stage = "read-event-handles"
	data, err := os.ReadFile(*lifecycleHandoffEvents)
	check("read event handle manifest", err)
	events := make(map[string]windows.Handle)
	check("decode event handle manifest", json.Unmarshal(data, &events))
	if len(events) != 4 {
		report.Error = "event handle manifest must contain exactly four owned handles"
		t.Fatal(report.Error)
	}
	seen := make(map[windows.Handle]bool)
	for _, label := range []string{"alive", "prepare", "ready", "exit"} {
		h := events[label]
		if h == 0 || h == windows.InvalidHandle || seen[h] {
			report.Error = "invalid or duplicate owned event handle: " + label
			t.Fatal(report.Error)
		}
		seen[h] = true
		defer windows.CloseHandle(h)
		state, err := windows.WaitForSingleObject(h, 0)
		check("probe event handle "+label, err)
		if state != uint32(windows.WAIT_TIMEOUT) {
			report.Error = "owned event was already signalled: " + label
			t.Fatal(report.Error)
		}
		report.EventHandlesVerified++
	}
	check("signal startup", windows.SetEvent(events["alive"]))
	waitLifecycleHandoffEvent(t, events["prepare"], "parent preparation request")
	report.Stage = "prepare-and-restore"
	readDACL := func() string {
		sd, err := windows.GetSecurityInfo(windows.CurrentProcess(), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
		check("read process DACL", err)
		return sd.String()
	}
	report.Before = readDACL()
	restore, err := prepareGUIHandoffAccess()
	check("prepare process access", err)
	report.Granted = readDACL()
	check("restore process access", restore())
	report.Restored = readDACL()
	if report.Before == report.Granted || report.Before != report.Restored {
		report.Error = "owned process DACL was not granted/restored exactly"
		t.Fatal(report.Error)
	}
	report.RestoreVerified = true
	report.Stage = "prepare-for-wait"
	restore, err = prepareGUIHandoffAccess()
	check("prepare second process access", err)
	defer func() {
		if err := restore(); err != nil {
			report.Error = "restore second handoff grant: " + err.Error()
			t.Errorf("restore second handoff grant: %v", err)
		}
	}()
	check("signal readiness", windows.SetEvent(events["ready"]))
	waitLifecycleHandoffEvent(t, events["exit"], "parent exit request")
	report.Stage = "completed"
}

func newLifecycleHandoffEvents(t *testing.T) map[string]windows.Handle {
	t.Helper()
	events := make(map[string]windows.Handle)
	for _, label := range []string{"alive", "prepare", "ready", "exit"} {
		// Anonymous, non-inheritable events have no cross-account name/ACL
		// lookup. Only explicitly duplicated wait/signal handles reach the child.
		h, err := windows.CreateEvent(nil, 1, 0, nil)
		if h != 0 {
			t.Cleanup(func() { _ = windows.CloseHandle(h) })
		}
		if err != nil {
			t.Fatalf("create owned handoff event: %v", err)
		}
		events[label] = h
	}
	return events
}

func waitLifecycleHandoffEvent(t *testing.T, event windows.Handle, label string) {
	t.Helper()
	state, err := windows.WaitForSingleObject(event, 10_000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("wait for handoff %s: status=%d error=%v", label, state, err)
	}
}

func waitLifecycleHandoffChildEvent(t *testing.T, event, child windows.Handle, label string) {
	t.Helper()
	state, err := windows.WaitForMultipleObjects([]windows.Handle{event, child}, false, 10_000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		var exit uint32
		exitErr := windows.GetExitCodeProcess(child, &exit)
		t.Fatalf("wait for handoff child %s: status=%d error=%v childExit=%d exitError=%v", label, state, err, exit, exitErr)
	}
}

func lifecycleHandoffEnvironment(t *testing.T) []uint16 {
	t.Helper()
	root, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	// A NULL environment creates the target user's profile environment, not
	// the caller's. Pass only these fixture opt-ins and Windows DLL base paths;
	// do not forward the CI runner's credentials or remaining environment.
	var block []uint16
	for _, entry := range []string{"SONDNS_ACCOUNT_TEST=1", "SONDNS_SERVICE_TEST=1", "SystemRoot=" + root, "WINDIR=" + root} {
		value, err := windows.UTF16FromString(entry)
		if err != nil {
			t.Fatal(err)
		}
		block = append(block, value...)
	}
	return append(block, 0)
}

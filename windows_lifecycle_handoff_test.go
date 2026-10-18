//go:build windows && !console

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var lifecycleHandoffEvents = flag.String("sondns-handoff-events", "", "owned account-fixture event prefix")
var lifecycleHandoffReport = flag.String("sondns-handoff-report", "", "owned account-fixture result file")

type lifecycleHandoffAccessReport struct {
	Before, Granted, Restored string
	RestoreVerified           bool
	Stage                     string
	Failed                    bool
	Error                     string
	NativeError, SessionID    uint32
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
	report.Stage = "open-events"
	events := make(map[string]windows.Handle)
	for _, label := range []string{"alive", "prepare", "ready", "exit"} {
		name, err := windows.UTF16PtrFromString(*lifecycleHandoffEvents + "." + label)
		check("encode event name", err)
		h, err := windows.OpenEvent(windows.SYNCHRONIZE|windows.EVENT_MODIFY_STATE, false, name)
		check("OpenEvent "+label, err)
		defer windows.CloseHandle(h)
		events[label] = h
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

func newLifecycleHandoffEvents(t *testing.T, prefix, sid string) map[string]windows.Handle {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x100002;;;%s)S:(ML;;NW;;;ME)", sid))
	if err != nil {
		t.Fatal(err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	events := make(map[string]windows.Handle)
	for _, label := range []string{"alive", "prepare", "ready", "exit"} {
		name, _ := windows.UTF16PtrFromString(prefix + "." + label)
		h, err := windows.CreateEvent(sa, 1, 0, name)
		if h != 0 {
			t.Cleanup(func() { _ = windows.CloseHandle(h) })
		}
		if err != nil {
			t.Fatalf("create unique owned handoff event: %v", err)
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

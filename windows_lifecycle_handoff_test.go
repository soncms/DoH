//go:build windows && !console

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
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
}

// This child only changes its own process DACL. It never starts a GUI, service,
// driver, credential prompt or resolver. Only the opted-in VM parent launches it.
func TestLifecycleGUIHandoffAccountChild(t *testing.T) {
	if *lifecycleHandoffEvents == "" {
		t.Skip("owned account process fixture")
	}
	if os.Getenv("SONDNS_ACCOUNT_TEST") != "1" || os.Getenv("SONDNS_SERVICE_TEST") != "1" ||
		*lifecycleHandoffReport == "" || windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("handoff child requires the opted-in standard-account fixture")
	}
	report := lifecycleHandoffAccessReport{Stage: "open-events"}
	defer func() {
		report.Failed = t.Failed()
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(*lifecycleHandoffReport, append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Errorf("write handoff child report: %v", err)
		}
	}()
	events := make(map[string]windows.Handle)
	for _, label := range []string{"alive", "prepare", "ready", "exit"} {
		name, err := windows.UTF16PtrFromString(*lifecycleHandoffEvents + "." + label)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.OpenEvent(windows.SYNCHRONIZE|windows.EVENT_MODIFY_STATE, false, name)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		events[label] = h
	}
	if err := windows.SetEvent(events["alive"]); err != nil {
		t.Fatal(err)
	}
	waitLifecycleHandoffEvent(t, events["prepare"], "parent preparation request")
	report.Stage = "prepare-and-restore"
	readDACL := func() string {
		sd, err := windows.GetSecurityInfo(windows.CurrentProcess(), windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return sd.String()
	}
	report.Before = readDACL()
	restore, err := prepareGUIHandoffAccess()
	if err != nil {
		t.Fatal(err)
	}
	report.Granted = readDACL()
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	report.Restored = readDACL()
	if report.Before == report.Granted || report.Before != report.Restored {
		t.Fatal("owned process DACL was not granted/restored exactly")
	}
	report.RestoreVerified = true
	report.Stage = "prepare-for-wait"
	restore, err = prepareGUIHandoffAccess()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restore(); err != nil {
			t.Errorf("restore second handoff grant: %v", err)
		}
	}()
	if err := windows.SetEvent(events["ready"]); err != nil {
		t.Fatal(err)
	}
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

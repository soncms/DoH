//go:build windows

package service

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type rejectNativeStartupBase struct{ scmStartupController }

func (rejectNativeStartupBase) SetStartupBase(uint32, []string) error {
	return windows.ERROR_ACCESS_DENIED
}

// This test creates/deletes a uniquely named, stopped SCM fixture. It never
// starts a process, opens WinDivert or mutates the actual SonDNS service.
func TestNativeSCMStartupTransaction(t *testing.T) {
	if os.Getenv("SONDNS_SCM_STARTUP_TEST") != "1" {
		t.Skip("set SONDNS_SCM_STARTUP_TEST=1 for the isolated stopped-service SCM test")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("SCM fixture requires an elevated test process")
	}
	before, err := QueryServiceSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Disconnect()
	name := "SonDNS-StartupTest-" + rand.Text()
	// A protected Windows binary is only a dormant SCM path. Never execute it.
	s, err := m.CreateService(name, filepath.Join(system, "svchost.exe"), mgr.Config{
		DisplayName: name, Description: "Temporary SonDNS startup API verification; never started",
		StartType: windows.SERVICE_DISABLED, ServiceType: windows.SERVICE_WIN32_OWN_PROCESS,
		ErrorControl: windows.SERVICE_ERROR_NORMAL,
	}, "-k", name)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	controller := scmStartupController{s}
	defer func() {
		if err := controller.SetStartupBase(windows.SERVICE_DISABLED, nil); err != nil {
			t.Errorf("disable temporary SCM fixture %s: %v", name, err)
		}
		if err := s.Delete(); err != nil {
			t.Errorf("delete temporary SCM fixture %s: %v", name, err)
		}
	}()
	initial, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	change, err := applyServiceStartupChange(controller, true)
	if err != nil || change == nil {
		t.Fatalf("enable startup: %v", err)
	}
	initialChange := change
	got, err := s.Config()
	if err != nil || !reflect.DeepEqual(got, serviceStartupConfig(initial, true)) {
		t.Fatalf("narrow update changed unrelated native settings: %+v %v", got, err)
	}
	if err := controller.SetDelayedAutoStart(true); err != nil {
		t.Fatal(err)
	}
	delayed, err := s.Config()
	if err != nil || !delayed.DelayedAutoStart {
		t.Fatalf("delayed startup fixture: %+v %v", delayed, err)
	}
	change, err = applyServiceStartupChange(controller, false)
	if err != nil || change == nil {
		t.Fatalf("disable startup: %v", err)
	}
	if err := change.rollback(controller); err != nil {
		t.Fatal(err)
	}
	got, err = s.Config()
	if err != nil || !reflect.DeepEqual(got, delayed) {
		t.Fatalf("native rollback did not restore delayed startup: %+v %v", got, err)
	}
	// The delayed-start step is real; inject a failure only at the base write.
	change, err = applyServiceStartupChange(rejectNativeStartupBase{controller}, false)
	if change != nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("native partial failure not surfaced: %v", err)
	}
	got, readErr := s.Config()
	if readErr != nil || !reflect.DeepEqual(got, delayed) {
		t.Fatalf("native partial failure was not rolled back: %+v %v", got, readErr)
	}
	// Also verify that a native rollback can remove the default dependencies.
	reset, err := applyServiceStartupChange(controller, true)
	if err != nil || reset == nil {
		t.Fatalf("clear delayed startup: %v", err)
	}
	if err := initialChange.rollback(controller); err != nil {
		t.Fatal(err)
	}
	got, err = s.Config()
	if err != nil || !reflect.DeepEqual(got, initial) {
		t.Fatalf("empty MULTI_SZ did not restore native configuration: %+v %v", got, err)
	}
	state, err := s.Query()
	if err != nil || state.State != svc.Stopped {
		t.Fatalf("fixture must remain stopped: %+v %v", state, err)
	}
	after, err := QueryServiceSnapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("real SonDNS service state changed: %+v -> %+v %v", before, after, err)
	}
	t.Logf("isolated stopped SCM fixture %s verified; real SonDNS service unchanged", name)
}

//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServiceStateTransitions(t *testing.T) {
	tests := []struct {
		s       serviceSnapshot
		text    string
		pending bool
	}{
		{serviceSnapshot{}, "not installed", false},
		{serviceSnapshot{true, svc.StartPending, windows.SERVICE_AUTO_START}, "starting", true},
		{serviceSnapshot{true, svc.Running, windows.SERVICE_AUTO_START}, "running · automatic", false},
		{serviceSnapshot{true, svc.Running, windows.SERVICE_DEMAND_START}, "running · manual", false},
		{serviceSnapshot{true, svc.StopPending, windows.SERVICE_AUTO_START}, "stopping", true},
		{serviceSnapshot{true, svc.Stopped, windows.SERVICE_AUTO_START}, "stopped", false},
	}
	for _, tt := range tests {
		if tt.s.text() != tt.text || tt.s.pending() != tt.pending {
			t.Errorf("%+v -> %s pending=%v", tt.s, tt.s.text(), tt.s.pending())
		}
	}
}

func TestServiceMutationRejectsAnotherInstallation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current := &testServiceConfig{value: mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run"})}}
	if err := requireThisService(current); err != nil {
		t.Fatalf("own service rejected: %v", err)
	}
	other := &testServiceConfig{value: mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{filepath.Join(t.TempDir(), "SonDNS.exe"), "run"})}}
	if err := requireThisService(other); err == nil {
		t.Fatal("service belonging to another portable folder was accepted")
	}
}

type testServiceConfig struct {
	value   mgr.Config
	updates int
}

func (s *testServiceConfig) Config() (mgr.Config, error) { return cloneServiceConfig(s.value), nil }
func (s *testServiceConfig) SetStartupBase(start uint32, dependencies []string) error {
	s.value.StartType, s.value.Dependencies = start, append([]string(nil), dependencies...)
	s.updates++
	return nil
}
func (s *testServiceConfig) SetDelayedAutoStart(enable bool) error {
	s.value.DelayedAutoStart = enable
	s.updates++
	return nil
}
func TestServiceStartupUpdatesPreserveOtherConfig(t *testing.T) {
	s := &testServiceConfig{value: mgr.Config{DisplayName: "SonDNS", BinaryPathName: "app.exe run", StartType: windows.SERVICE_DEMAND_START}}
	if err := updateServiceStartup(s, true); err != nil {
		t.Fatal(err)
	}
	if s.value.StartType != windows.SERVICE_AUTO_START || s.value.BinaryPathName != "app.exe run" || s.value.DisplayName != "SonDNS" {
		t.Fatalf("unexpected config: %+v", s.value)
	}
	_ = updateServiceStartup(s, true)
	if s.updates != 1 {
		t.Fatal("unchanged startup mutated")
	}
	_ = updateServiceStartup(s, false)
	if s.value.StartType != windows.SERVICE_DEMAND_START || s.updates != 2 {
		t.Fatal("manual mode not applied")
	}
}

func TestServiceStartupRollbackPreservesConcurrentConfiguration(t *testing.T) {
	previous := mgr.Config{
		DisplayName: "SonDNS", BinaryPathName: `F:\SonDNS\SonDNS.exe run`,
		StartType: windows.SERVICE_DISABLED, DelayedAutoStart: true,
		Dependencies: []string{"Tcpip"}, Description: "original description",
	}
	for _, scenario := range []string{"unchanged", "start_type", "delayed_start", "dependencies", "binary", "description"} {
		t.Run(scenario, func(t *testing.T) {
			s := &testServiceConfig{value: cloneServiceConfig(previous)}
			change, err := applyServiceStartupChange(s, true)
			if err != nil || change == nil {
				t.Fatalf("apply startup: %v", err)
			}
			current := cloneServiceConfig(s.value)
			switch scenario {
			case "start_type":
				current.StartType = windows.SERVICE_DEMAND_START
			case "delayed_start":
				current.DelayedAutoStart = true
			case "dependencies":
				current.Dependencies = []string{"OtherDependency"}
			case "binary":
				current.BinaryPathName = `F:\OtherFolder\SonDNS.exe run`
			case "description":
				current.Description = "changed by another administrator"
			}
			s.value = current
			s.updates = 0
			err = change.rollback(s)
			if scenario == "unchanged" {
				if err != nil || s.updates != 2 || !reflect.DeepEqual(s.value, previous) {
					t.Fatalf("prior SCM configuration was not restored: %+v %v", s.value, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "changed concurrently") || s.updates != 0 || !reflect.DeepEqual(s.value, current) {
				t.Fatalf("rollback overwrote a concurrent SCM change: %+v %v", s.value, err)
			}
		})
	}
}

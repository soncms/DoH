//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"testing"
)

func TestServiceStateTransitions(t *testing.T) {
	tests := []struct {
		s       serviceSnapshot
		text    string
		pending bool
	}{
		{serviceSnapshot{}, "not installed", false},
		{serviceSnapshot{Exists: true, State: svc.StartPending, StartType: windows.SERVICE_AUTO_START}, "starting", true},
		{serviceSnapshot{Exists: true, State: svc.Running, StartType: windows.SERVICE_AUTO_START}, "running · automatic", false},
		{serviceSnapshot{Exists: true, State: svc.Running, StartType: windows.SERVICE_DEMAND_START}, "running · manual", false},
		{serviceSnapshot{Exists: true, State: svc.StopPending, StartType: windows.SERVICE_AUTO_START}, "stopping", true},
		{serviceSnapshot{Exists: true, State: svc.Stopped, StartType: windows.SERVICE_AUTO_START}, "stopped", false},
	}
	for _, tt := range tests {
		if tt.s.text() != tt.text || tt.s.pending() != tt.pending {
			t.Errorf("%+v -> %s pending=%v", tt.s, tt.s.text(), tt.s.pending())
		}
	}
}

type testServiceConfig struct {
	value   mgr.Config
	updates int
}

func (s *testServiceConfig) Config() (mgr.Config, error)     { return s.value, nil }
func (s *testServiceConfig) UpdateConfig(c mgr.Config) error { s.value = c; s.updates++; return nil }
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

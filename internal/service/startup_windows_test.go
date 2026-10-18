//go:build windows

package service

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

type failingStartupController struct {
	testServiceConfig
	operations []string
	failCalls  []int
	reads      int
	failRead   int
	afterDelay func()
	beforeBase func()
}

func (s *failingStartupController) Config() (mgr.Config, error) {
	s.reads++
	if s.reads == s.failRead {
		return mgr.Config{}, errors.New("SCM read failed")
	}
	return s.testServiceConfig.Config()
}

func (s *failingStartupController) record(operation string) error {
	s.operations = append(s.operations, operation)
	if slices.Contains(s.failCalls, len(s.operations)) {
		return fmt.Errorf("SCM write %d failed", len(s.operations))
	}
	return nil
}

func (s *failingStartupController) SetStartupBase(start uint32, dependencies []string) error {
	if err := s.record(fmt.Sprintf("base:%d", start)); err != nil {
		return err
	}
	if s.beforeBase != nil {
		s.beforeBase()
	}
	return s.testServiceConfig.SetStartupBase(start, dependencies)
}

func (s *failingStartupController) SetDelayedAutoStart(enable bool) error {
	if err := s.record(fmt.Sprintf("delayed:%t", enable)); err != nil {
		return err
	}
	err := s.testServiceConfig.SetDelayedAutoStart(enable)
	if s.afterDelay != nil {
		s.afterDelay()
	}
	return err
}

func TestServiceDependencyBlockUsesWindowsMultiSZ(t *testing.T) {
	for _, tt := range []struct {
		names []string
		want  []uint16
	}{
		{nil, []uint16{0, 0}},
		{[]string{}, []uint16{0, 0}},
		{[]string{"Tcpip", "Dnscache"}, []uint16{'T', 'c', 'p', 'i', 'p', 0, 'D', 'n', 's', 'c', 'a', 'c', 'h', 'e', 0, 0}},
		{[]string{"服务"}, []uint16{0x670d, 0x52a1, 0, 0}},
	} {
		got, err := serviceDependencyBlock(tt.names)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Fatalf("dependency block for %q: %v %v", tt.names, got, err)
		}
	}
	for _, names := range [][]string{{""}, {"Tcpip", ""}, {"Tcpip\x00Other"}} {
		if _, err := serviceDependencyBlock(names); err == nil {
			t.Fatalf("invalid dependency accepted: %q", names)
		}
	}
}

func TestServiceStartupPartialFailureRollsBackCompletedSteps(t *testing.T) {
	for _, tt := range []struct {
		name      string
		failCalls []int
		failRead  int
		wantOps   []string
		restored  bool
	}{
		{"first_write", []int{1}, 0, []string{"delayed:false"}, true},
		{"base_write", []int{2}, 0, []string{"delayed:false", "base:3", "delayed:true"}, true},
		{"read_before_base", nil, 2, []string{"delayed:false", "delayed:true"}, true},
		{"rollback_write", []int{2, 3}, 0, []string{"delayed:false", "base:3", "delayed:true"}, false},
		{"initial_read", nil, 1, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			previous := mgr.Config{StartType: windows.SERVICE_AUTO_START, DelayedAutoStart: true,
				Dependencies: []string{"Tcpip"}, BinaryPathName: "original.exe", Description: "preserve"}
			s := &failingStartupController{testServiceConfig: testServiceConfig{value: cloneServiceConfig(previous)}, failCalls: tt.failCalls, failRead: tt.failRead}
			change, err := applyServiceStartupChange(s, false)
			if err == nil || change != nil || !slices.Equal(s.operations, tt.wantOps) {
				t.Fatalf("partial failure was not reported/rolled back: %v operations=%v", err, s.operations)
			}
			if tt.restored && !reflect.DeepEqual(s.value, previous) {
				t.Fatalf("completed changes were not restored: %+v", s.value)
			}
			if !tt.restored && (s.value.DelayedAutoStart || !strings.Contains(err.Error(), "restore delayed") || !strings.Contains(err.Error(), "write 2 failed")) {
				t.Fatalf("original/rollback failure was hidden: %+v %v", s.value, err)
			}
		})
	}
}

func TestServiceStartupUndoRestoresExactStateAndIsIdempotent(t *testing.T) {
	previous := mgr.Config{StartType: windows.SERVICE_AUTO_START, DelayedAutoStart: true,
		BinaryPathName: "original.exe", ServiceStartName: "LocalSystem", Description: "unchanged", SidType: 1}
	s := &failingStartupController{testServiceConfig: testServiceConfig{value: cloneServiceConfig(previous)}}
	change, err := applyServiceStartupChange(s, false)
	if err != nil || change == nil || s.value.DelayedAutoStart || s.value.StartType != windows.SERVICE_DEMAND_START {
		t.Fatalf("apply: %+v %v", s.value, err)
	}
	if err := change.rollback(s); err != nil || !reflect.DeepEqual(s.value, previous) {
		t.Fatalf("undo: %+v %v", s.value, err)
	}
	if !slices.Equal(s.operations, []string{"delayed:false", "base:3", "base:2", "delayed:true"}) {
		t.Fatalf("invalid rollback order: %v", s.operations)
	}
	reads, writes := s.reads, len(s.operations)
	if err := change.rollback(s); err != nil || s.reads != reads || len(s.operations) != writes {
		t.Fatalf("second undo repeated changes: %v", err)
	}
}

func TestServiceStartupDetectsConcurrentChangesBetweenSteps(t *testing.T) {
	s := &failingStartupController{testServiceConfig: testServiceConfig{value: mgr.Config{
		StartType: windows.SERVICE_AUTO_START, DelayedAutoStart: true, Dependencies: []string{"Tcpip"}}}}
	s.afterDelay = func() { s.value.Description = "changed concurrently" }
	change, err := applyServiceStartupChange(s, false)
	if err == nil || change != nil || !strings.Contains(err.Error(), "changed concurrently") ||
		s.value.Description != "changed concurrently" || s.value.StartType != windows.SERVICE_AUTO_START || len(s.operations) != 1 {
		t.Fatalf("concurrent change was overwritten: %+v operations=%v err=%v", s.value, s.operations, err)
	}
}

func TestServiceStartupNarrowWritesPreserveUnrelatedLateChanges(t *testing.T) {
	s := &failingStartupController{testServiceConfig: testServiceConfig{value: mgr.Config{
		StartType: windows.SERVICE_DEMAND_START, Dependencies: []string{"Tcpip"},
		BinaryPathName: "original.exe", ServiceStartName: "LocalSystem", Description: "previous", SidType: 1}}}
	s.beforeBase = func() { s.value.Description = "changed after the last read" }
	change, err := applyServiceStartupChange(s, true)
	if err != nil || change == nil || s.value.Description != "changed after the last read" ||
		s.value.BinaryPathName != "original.exe" || s.value.ServiceStartName != "LocalSystem" || s.value.SidType != 1 {
		t.Fatalf("startup update overwrote unrelated fields: %+v %v", s.value, err)
	}
	if err := change.rollback(s); err == nil || len(s.operations) != 1 {
		t.Fatalf("undo overwrote the late concurrent change: %+v %v", s.value, err)
	}
}

func TestServiceStartupRollbackReadAndWriteErrorsAreVisible(t *testing.T) {
	for _, scenario := range []string{"read", "base_write"} {
		t.Run(scenario, func(t *testing.T) {
			s := &failingStartupController{testServiceConfig: testServiceConfig{value: mgr.Config{
				StartType: windows.SERVICE_DEMAND_START, Dependencies: []string{"Tcpip"}}}}
			change, err := applyServiceStartupChange(s, true)
			if err != nil || change == nil {
				t.Fatal(err)
			}
			if scenario == "read" {
				s.failRead = s.reads + 1
			} else {
				s.failCalls = []int{len(s.operations) + 1}
			}
			if err := change.rollback(s); err == nil || s.value.StartType != windows.SERVICE_AUTO_START {
				t.Fatalf("rollback failure was hidden: %+v %v", s.value, err)
			}
		})
	}
}

//go:build windows && !console

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

type stoppedMigrationFixture struct{ scmMigrationService }

func (stoppedMigrationFixture) StartAndWait() error {
	return errors.New("fixture must never start a process")
}

// Uses a uniquely named disabled SCM fixture, never the installed SonDNS
// service. No process/driver is started and no real Run value is touched.
func TestNativeSCMMigrationTransaction(t *testing.T) {
	if os.Getenv("SONDNS_SCM_MIGRATION_TEST") != "1" {
		t.Skip("set SONDNS_SCM_MIGRATION_TEST=1 for the isolated stopped-service migration test")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("SCM fixture requires elevation")
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
	name := "SonDNS-MigrationTest-" + rand.Text()
	s, err := m.CreateService(name, filepath.Join(system, "svchost.exe"), mgr.Config{
		DisplayName: name, Description: "Temporary stopped SonDNS migration fixture; never started",
		ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: windows.SERVICE_DISABLED,
		ErrorControl: windows.SERVICE_ERROR_NORMAL, Dependencies: []string{"Tcpip"},
	}, "-k", name)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer func() {
		if err := s.Delete(); err != nil {
			t.Errorf("delete fixture %s: %v", name, err)
		}
	}()
	initial, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	service := stoppedMigrationFixture{scmMigrationService{s}}
	p := serviceMigrationPlan{Previous: initial, State: svc.Stopped, TargetExe: filepath.Join(system, "svchost.exe"), TargetConfig: filepath.Join(system, "SonDNS unused test", "sondns.json")}
	for _, fail := range []bool{true, false} {
		undone, journal := false, ""
		_, err := executeServiceMigration(p, service, migrationHooks{
			Validate: func() error { return nil },
			Stage: func() (migrationStage, error) {
				return migrationStage{Directory: "synthetic fixture backup", Undo: func() error { undone = true; return nil }, Journal: func(status string, _ error) error { journal = status; return nil }}, nil
			},
			ChangeTray: func() (func() error, error) {
				if fail {
					return nil, errMigrationTest
				}
				return nil, nil
			},
		})
		got, readErr := s.Config()
		if readErr != nil {
			t.Fatal(readErr)
		}
		expected := cloneServiceConfig(initial)
		if fail {
			if !errors.Is(err, errMigrationTest) || !undone || journal != "rolled-back" {
				t.Fatalf("native rollback: %s %v", journal, err)
			}
		} else {
			if err != nil || undone || journal != "complete" {
				t.Fatalf("native migration: %s %v", journal, err)
			}
			expected.BinaryPathName = migrationCommand(p)
			if err := requireServiceConfigForExe(s, p.TargetExe, p.TargetConfig); err != nil {
				t.Fatalf("SCM custom config binding rejected: %v", err)
			}
			if err := requireServiceConfigForExe(s, p.TargetExe, defaultConfigForExe(p.TargetExe)); err == nil {
				t.Fatal("SCM binding accepted wrong config for the same EXE")
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("native binding write changed unrelated config: %+v -> %+v", expected, got)
		}
		state, err := s.Query()
		if err != nil || state.State != svc.Stopped {
			t.Fatalf("fixture did not stay stopped: %+v %v", state, err)
		}
	}
	after, err := QueryServiceSnapshot()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("actual SonDNS service changed: %+v -> %+v %v", before, after, err)
	}
	t.Logf("narrow binding change/rollback verified on disabled stopped fixture %s; actual SonDNS service unchanged", name)
}

//go:build windows && !console

package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var errMigrationTest = errors.New("injected migration failure")

type fakeMigrationService struct {
	config                           mgr.Config
	state                            svc.State
	failStop, failStart, failBind    int
	stopCalls, startCalls, bindCalls int
	partial                          bool
	afterStart                       func()
}

func (s *fakeMigrationService) Config() (mgr.Config, error) { return cloneServiceConfig(s.config), nil }
func (s *fakeMigrationService) Query() (svc.Status, error)  { return svc.Status{State: s.state}, nil }
func (s *fakeMigrationService) StopAndWait() error {
	s.stopCalls++
	if s.failStop == s.stopCalls {
		if s.partial {
			s.state = svc.Stopped
		}
		return errMigrationTest
	}
	s.state = svc.Stopped
	return nil
}
func (s *fakeMigrationService) StartAndWait() error {
	s.startCalls++
	if s.failStart == s.startCalls {
		if s.partial {
			s.state = svc.Running
		}
		return errMigrationTest
	}
	s.state = svc.Running
	if s.afterStart != nil {
		s.afterStart()
	}
	return nil
}
func (s *fakeMigrationService) SetBinding(command string) error {
	s.bindCalls++
	if s.failBind == s.bindCalls {
		return errMigrationTest
	}
	s.config.BinaryPathName = command
	return nil
}

func testMigrationPlan() serviceMigrationPlan {
	return serviceMigrationPlan{
		Previous: mgr.Config{BinaryPathName: `C:\Old\SonDNS.exe run --config=C:\Old\data\sondns.json`,
			ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: windows.SERVICE_AUTO_START,
			ServiceStartName: "LocalSystem", Description: "preserve", Dependencies: []string{"Tcpip"}, DelayedAutoStart: true},
		State: svc.Running, TargetExe: `F:\New Folder\SonDNS.exe`, TargetConfig: `F:\New Folder\data\sondns.json`,
		CanRestartSource: true, RecoveryLimit: "unsafe source must remain stopped", Settings: DefaultConfig(),
	}
}

func TestServiceMigrationTransaction(t *testing.T) {
	for _, scenario := range []string{"running", "stopped", "validate-initial", "stage", "validate-staged", "journal-switching", "stop", "partial-stop", "bind", "journal-bound", "validate-bound", "start", "partial-start", "tray", "journal-complete", "rollback-stop", "rollback-bind", "undo-config", "undo-tray", "unsafe-source", "source-validation", "concurrent-binding", "concurrent-after-tray"} {
		t.Run(scenario, func(t *testing.T) {
			p := testMigrationPlan()
			if scenario == "stopped" {
				p.State = svc.Stopped
			}
			if scenario == "unsafe-source" {
				p.CanRestartSource = false
			}
			s := &fakeMigrationService{config: cloneServiceConfig(p.Previous), state: p.State}
			switch scenario {
			case "stop", "partial-stop":
				s.failStop = 1
				s.partial = scenario == "partial-stop"
			case "bind":
				s.failBind = 1
			case "start", "partial-start":
				s.failStart = 1
				s.partial = scenario == "partial-start"
			case "rollback-stop":
				s.failStart = 1
				s.failStop = 2
			case "rollback-bind":
				s.failStart = 1
				s.failBind = 2
			case "concurrent-binding":
				s.afterStart = func() { s.config.Description = "external admin" }
			}
			staged, undone, tray, undoneTray, validates := false, false, false, false, 0
			journal := ""
			hooks := migrationHooks{
				Validate: func() error {
					validates++
					if scenario == "validate-initial" && validates == 1 || scenario == "validate-staged" && validates == 2 || scenario == "validate-bound" && validates == 3 {
						return errMigrationTest
					}
					return nil
				},
				Stage: func() (migrationStage, error) {
					if scenario == "stage" {
						return migrationStage{}, errMigrationTest
					}
					staged = true
					return migrationStage{Directory: "backup", Undo: func() error {
						if scenario == "undo-config" {
							return errors.New("config recovery failure")
						}
						undone = true
						return nil
					}, Journal: func(status string, cause error) error {
						journal = status
						if scenario == "journal-"+status {
							return errMigrationTest
						}
						return nil
					}}, nil
				},
				ChangeTray: func() (func() error, error) {
					switch scenario {
					case "tray", "unsafe-source", "source-validation", "undo-config":
						return nil, errMigrationTest
					case "concurrent-after-tray":
						s.config.BinaryPathName = "external executable"
					}
					tray = true
					return func() error {
						if scenario == "undo-tray" {
							return errors.New("tray recovery failure")
						}
						undoneTray = true
						tray = false
						return nil
					}, nil
				},
				ValidateSource: func() error {
					if scenario == "source-validation" {
						return errMigrationTest
					}
					return nil
				},
			}
			if scenario == "undo-tray" {
				oldStage := hooks.Stage
				hooks.Stage = func() (migrationStage, error) {
					stage, err := oldStage()
					oldJournal := stage.Journal
					stage.Journal = func(status string, cause error) error {
						if status == "complete" {
							return errMigrationTest
						}
						return oldJournal(status, cause)
					}
					return stage, err
				}
			}
			backup, err := executeServiceMigration(p, s, hooks)
			if scenario == "running" || scenario == "stopped" {
				if err != nil || backup != "backup" || journal != "complete" || !staged || undone || !tray || s.state != p.State {
					t.Fatalf("incomplete success: %+v %s %v", s, journal, err)
				}
				expected := cloneServiceConfig(p.Previous)
				expected.BinaryPathName = migrationCommand(p)
				if !reflect.DeepEqual(s.config, expected) {
					t.Fatal("migration changed unrelated SCM fields")
				}
				if scenario == "stopped" && (s.stopCalls != 0 || s.startCalls != 0) {
					t.Fatal("stopped migration touched service process")
				}
				return
			}
			if err == nil {
				t.Fatal("injected failure was hidden")
			}
			if scenario == "validate-initial" || scenario == "stage" {
				if staged || s.stopCalls != 0 || s.bindCalls != 0 {
					t.Fatal("preparation failure interrupted service")
				}
				return
			}
			if !strings.Contains(err.Error(), "backup") {
				t.Fatalf("missing recovery location: %v", err)
			}
			if scenario == "concurrent-binding" || scenario == "concurrent-after-tray" || scenario == "rollback-stop" || scenario == "rollback-bind" {
				if undone || s.config.BinaryPathName == p.Previous.BinaryPathName || journal != "recovery-required" {
					t.Fatalf("unsafe rollback changed config or binding: %+v %s %v", s, journal, err)
				}
				return
			}
			if !reflect.DeepEqual(s.config, p.Previous) {
				t.Fatal("previous SCM configuration not restored")
			}
			if !undone && scenario != "undo-config" {
				t.Fatal("destination config not restored")
			}
			if scenario == "unsafe-source" || scenario == "source-validation" {
				if s.state != svc.Stopped || s.startCalls != 1 || journal != "recovery-required" {
					t.Fatalf("unsafe source restarted: %+v %v", s, err)
				}
				return
			}
			if s.state != p.State {
				t.Fatalf("original service state not restored: %+v %v", s, err)
			}
			if scenario == "journal-complete" && !undoneTray {
				t.Fatal("tray rollback missing")
			}
			if scenario == "undo-tray" && !strings.Contains(err.Error(), "tray recovery failure") || scenario == "undo-config" && !strings.Contains(err.Error(), "config recovery failure") {
				t.Fatal("rollback error was hidden")
			}
		})
	}
}

func TestServiceMigrationRejectsChangedPreview(t *testing.T) {
	p := testMigrationPlan()
	for _, scenario := range []string{"config", "state"} {
		s := &fakeMigrationService{config: cloneServiceConfig(p.Previous), state: p.State}
		if scenario == "config" {
			s.config.Dependencies = []string{"external"}
		} else {
			s.state = svc.Stopped
		}
		_, err := executeServiceMigration(p, s, migrationHooks{Validate: func() error { return nil }, Stage: func() (migrationStage, error) {
			t.Fatal("changed preview reached staging")
			return migrationStage{}, nil
		}})
		if err == nil {
			t.Fatal("changed preview accepted")
		}
	}
}

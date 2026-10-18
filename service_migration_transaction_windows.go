//go:build windows && !console

package main

import (
	"errors"
	"fmt"
	"reflect"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type migrationService interface {
	Config() (mgr.Config, error)
	Query() (svc.Status, error)
	StopAndWait() error
	StartAndWait() error
	SetBinding(string) error
}

type migrationStage struct {
	Directory string
	Undo      func() error
	Journal   func(string, error) error
}

type migrationHooks struct {
	Validate       func() error
	Stage          func() (migrationStage, error)
	ChangeTray     func() (func() error, error)
	ValidateSource func() error
}

func migrationCommand(p serviceMigrationPlan) string {
	return windows.ComposeCommandLine([]string{p.TargetExe, "run", "--config=" + p.TargetConfig})
}

func checkMigrationBinding(s migrationService, expected mgr.Config) error {
	current, err := s.Config()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, expected) {
		return errors.New("service configuration changed concurrently; migration did not overwrite it")
	}
	return nil
}

func executeServiceMigration(p serviceMigrationPlan, s migrationService, hooks migrationHooks) (string, error) {
	if err := hooks.Validate(); err != nil {
		return "", err
	}
	if err := checkMigrationBinding(s, p.Previous); err != nil {
		return "", err
	}
	state, err := s.Query()
	if err != nil || state.State != p.State {
		return "", errors.Join(errors.New("service state changed; review migration again"), err)
	}
	stage, err := hooks.Stage()
	if err != nil {
		return "", err
	}
	bound, stopRequested := false, false
	applied := cloneServiceConfig(p.Previous)
	var undoTray func() error
	rollback := func(cause error) (string, error) {
		var recovery error
		if err := checkMigrationBinding(s, applied); err != nil {
			recovery = err // Keep candidate config if another binding may still use it.
		} else {
			if bound {
				if err := s.StopAndWait(); err != nil {
					recovery = fmt.Errorf("stop migration candidate before rollback: %w", err)
				} else if err := checkMigrationBinding(s, applied); err != nil {
					recovery = err
				} else if err := s.SetBinding(p.Previous.BinaryPathName); err != nil {
					recovery = fmt.Errorf("restore previous service binding: %w", err)
				} else {
					bound = false
					applied = cloneServiceConfig(p.Previous)
				}
			}
			if !bound {
				if undoTray != nil {
					recovery = errors.Join(recovery, undoTray())
				}
				if err := checkMigrationBinding(s, applied); err != nil {
					combined := errors.Join(cause, recovery, err)
					combined = errors.Join(combined, stage.Journal("recovery-required", combined))
					return stage.Directory, fmt.Errorf("migration failed; backup/recovery report: %s: %w", stage.Directory, combined)
				}
				recovery = errors.Join(recovery, stage.Undo())
				if stopRequested && p.State == svc.Running {
					current, queryErr := s.Query()
					if queryErr != nil {
						recovery = errors.Join(recovery, queryErr)
					} else if current.State != svc.Running {
						if current.State != svc.Stopped {
							recovery = errors.Join(recovery, errors.New("old service is still transitioning; automatic restart was not attempted"))
						} else if !p.CanRestartSource {
							recovery = errors.Join(recovery, errors.New(p.RecoveryLimit))
						} else if err := hooks.ValidateSource(); err != nil {
							recovery = errors.Join(recovery, fmt.Errorf("old folder is no longer safe for automatic restart: %w", err))
						} else if err := checkMigrationBinding(s, p.Previous); err != nil {
							recovery = errors.Join(recovery, err)
						} else {
							recovery = errors.Join(recovery, s.StartAndWait())
						}
					}
				}
			}
		}
		status := "rolled-back"
		if recovery != nil {
			status = "recovery-required"
		}
		combined := errors.Join(cause, recovery)
		combined = errors.Join(combined, stage.Journal(status, combined))
		return stage.Directory, fmt.Errorf("migration failed; backup/recovery report: %s: %w", stage.Directory, combined)
	}
	if err := checkMigrationBinding(s, applied); err != nil {
		return rollback(err)
	}
	state, err = s.Query()
	if err != nil || state.State != p.State {
		return rollback(errors.Join(errors.New("service state changed while preparing migration"), err))
	}
	if err := hooks.Validate(); err != nil {
		return rollback(err)
	}
	if err := stage.Journal("switching", nil); err != nil {
		return rollback(err)
	}
	if p.State == svc.Running {
		stopRequested = true
		if err := s.StopAndWait(); err != nil {
			return rollback(err)
		}
	}
	if err := checkMigrationBinding(s, applied); err != nil {
		return rollback(err)
	}
	if err := s.SetBinding(migrationCommand(p)); err != nil {
		return rollback(err)
	}
	bound = true
	applied.BinaryPathName = migrationCommand(p)
	if err := stage.Journal("bound", nil); err != nil {
		return rollback(err)
	}
	if p.State == svc.Running {
		if err := hooks.Validate(); err != nil {
			return rollback(err)
		}
		if err := checkMigrationBinding(s, applied); err != nil {
			return rollback(err)
		}
		if err := s.StartAndWait(); err != nil {
			return rollback(err)
		}
	}
	if err := checkMigrationBinding(s, applied); err != nil {
		return rollback(err)
	}
	undoTray, err = hooks.ChangeTray()
	if err != nil {
		return rollback(err)
	}
	if err := checkMigrationBinding(s, applied); err != nil {
		return rollback(err)
	}
	state, err = s.Query()
	if err != nil || state.State != p.State {
		return rollback(errors.Join(errors.New("service did not retain the expected migration state"), err))
	}
	if err := stage.Journal("complete", nil); err != nil {
		return rollback(err)
	}
	return stage.Directory, nil
}

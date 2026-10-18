//go:build windows && !console

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The sole explicit exception to normal service ownership checks. Callers must
// use a reviewed plan; executeServiceMigration rechecks the complete SCM snapshot.
func openMigrationService(access uint32) (*mgr.Service, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return nil, errors.New("administrator access is required for migration")
	}
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(h)
	name, _ := windows.UTF16PtrFromString(serviceName)
	sh, err := windows.OpenService(h, name, access)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: serviceName, Handle: sh}, nil
}

func inspectServiceMigration(configPath string) (serviceMigrationPlan, error) {
	s, err := openMigrationService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return serviceMigrationPlan{}, err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return serviceMigrationPlan{}, err
	}
	st, err := s.Query()
	if err != nil {
		return serviceMigrationPlan{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return serviceMigrationPlan{}, err
	}
	p, err := prepareServiceMigration(c, st.State, exe, configPath)
	if err != nil {
		return p, err
	}
	p.Tray, err = readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey)
	if err == nil {
		err = validateMigrationTray(p.Tray, p.SourceExe, p.TargetExe)
	}
	return p, err
}

type scmMigrationService struct{ *mgr.Service }

func (s scmMigrationService) SetBinding(command string) error {
	p, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return err
	}
	// Do not pass a service account, password, dependencies or display name.
	return windows.ChangeServiceConfig(s.Handle, windows.SERVICE_NO_CHANGE, windows.SERVICE_NO_CHANGE,
		windows.SERVICE_NO_CHANGE, p, nil, nil, nil, nil, nil, nil)
}

func (s scmMigrationService) StopAndWait() error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if st.State != svc.Running {
			return errors.New("service is transitioning; wait before stopping it")
		}
		if _, err := s.Control(svc.Stop); err != nil {
			return err
		}
	}
	return waitForServiceStopped(s.Service, 20*time.Second)
}

func (s scmMigrationService) StartAndWait() error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		return nil
	}
	if st.State != svc.Stopped {
		return errors.New("service is transitioning; automatic start was not attempted")
	}
	if err := s.Start(); err != nil {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, err = s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			return nil
		}
		if st.State != svc.StartPending {
			return fmt.Errorf("service did not start: state=%d Win32ExitCode=%d ServiceExitCode=%d", st.State, st.Win32ExitCode, st.ServiceSpecificExitCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("service start timed out; inspect the recovery report")
}

func validateMigrationFiles(p serviceMigrationPlan) error {
	root := filepath.Dir(p.TargetExe)
	if err := validatePortableServiceFolder(root); err != nil {
		return err
	}
	for _, f := range []struct {
		root, path, hash string
		limit            int64
	}{
		{filepath.Dir(p.SourceExe), p.SourceExe, p.SourceExeHash, 128 << 20},
		{filepath.Dir(p.SourceExe), p.SourceConfig, p.SourceConfigHash, 4 << 20},
		{root, p.TargetExe, p.TargetExeHash, 128 << 20},
	} {
		data, err := readMigrationFile(f.root, f.path, f.limit)
		if err != nil {
			return err
		}
		if migrationHash(data) != f.hash {
			return fmt.Errorf("migration file changed; review it again: %s", f.path)
		}
	}
	return nil
}

func migrateExistingService(p serviceMigrationPlan) (string, error) {
	s, err := openMigrationService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS | windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	if err != nil {
		return "", err
	}
	defer s.Close()
	return executeServiceMigration(p, scmMigrationService{s}, nativeMigrationHooks(p))
}

// Keep the disposable live-migration test on the same file validation, backup
// and registry implementation used by the confirmed GUI migration.
func nativeMigrationHooks(p serviceMigrationPlan) migrationHooks {
	return migrationHooks{
		Validate: func() error { return validateMigrationFiles(p) },
		Stage:    func() (migrationStage, error) { return stageServiceMigration(p) },
		ChangeTray: func() (func() error, error) {
			return changeMigrationTrayAt(registry.LOCAL_MACHINE, runRegistryKey, p.Tray, p.TargetExe, p.TargetConfig, p.Settings.ServiceAutoStart)
		},
		ValidateSource: func() error {
			if err := validatePortableServiceFolder(filepath.Dir(p.SourceExe)); err != nil {
				return err
			}
			return validateMigrationFiles(p)
		},
	}
}

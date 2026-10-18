//go:build windows && !console

package service

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type lifecycleMigrationAccessReport struct {
	Operation                                       string
	RequestedAccess, ErrorCode                      uint32
	Error                                           string
	StopCalls, BindCalls, StartCalls                int
	StoppedBeforeBind                               svc.Status
	Before, After                                   mgr.Config
	ServiceBefore, ServiceAfter                     svc.Status
	TrayAfter                                       migrationTrayValue
	SourceBeforeSHA256, SourceAfterSHA256           string
	DestinationBeforeSHA256, DestinationAfterSHA256 string
	DPIBefore, DPIAfter                             uint32
	Backup                                          lifecycleMigrationBackupReport
	DNSVerified, Verified                           bool
}

// Restrict only this owned service handle, not any account, service DACL or
// machine policy. Every operation still calls the production SCM implementation;
// access-denied errors come from Windows, never an injected error callback.
type limitedLifecycleMigration struct {
	scmMigrationService
	report *lifecycleMigrationAccessReport
}

func (s *limitedLifecycleMigration) StopAndWait() error {
	s.report.StopCalls++
	err := s.scmMigrationService.StopAndWait()
	if err != nil {
		s.recordError(err)
	} else {
		status, queryErr := s.Query()
		if queryErr != nil {
			return queryErr
		}
		s.report.StoppedBeforeBind = status
	}
	return err
}

func (s *limitedLifecycleMigration) SetBinding(command string) error {
	s.report.BindCalls++
	err := s.scmMigrationService.SetBinding(command)
	if err != nil {
		s.recordError(err)
	}
	return err
}

func (s *limitedLifecycleMigration) StartAndWait() error {
	s.report.StartCalls++
	return s.scmMigrationService.StartAndWait()
}

func (s *limitedLifecycleMigration) recordError(err error) {
	s.report.Error = err.Error()
	var code windows.Errno
	if errors.As(err, &code) {
		s.report.ErrorCode = uint32(code)
	}
}

// Called only inside the opted-in disposable running-service lifecycle fixture.
func runLifecycleMigrationAccessFailures(t *testing.T, owner *mgr.Service, p serviceMigrationPlan,
	destinationBefore []byte, backups map[string]string, reports *[]lifecycleMigrationAccessReport) {
	t.Helper()
	for _, operation := range []string{"stop", "rebind"} {
		access := uint32(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS | windows.SERVICE_START)
		if operation == "stop" {
			access |= windows.SERVICE_CHANGE_CONFIG // No SERVICE_STOP.
		} else {
			access |= windows.SERVICE_STOP // No SERVICE_CHANGE_CONFIG.
		}
		*reports = append(*reports, lifecycleMigrationAccessReport{Operation: operation, RequestedAccess: access})
		runLifecycleMigrationAccessFailure(t, owner, p, destinationBefore, backups, &(*reports)[len(*reports)-1])
	}
}

func runLifecycleMigrationAccessFailure(t *testing.T, owner *mgr.Service, p serviceMigrationPlan,
	destinationBefore []byte, backups map[string]string, report *lifecycleMigrationAccessReport) {
	t.Helper()
	report.Before = requireLifecycleMigrationService(t, owner, p.Previous)
	before, err := owner.Query()
	if err != nil || before.ProcessId == 0 {
		t.Fatalf("access-failure source process unavailable: %+v %v", before, err)
	}
	report.ServiceBefore = before
	sourceBefore := readLifecycleMigrationFile(t, p.SourceConfig)
	if migrationHash(sourceBefore) != p.SourceConfigHash ||
		!bytes.Equal(readLifecycleMigrationFile(t, p.TargetConfig), destinationBefore) {
		t.Fatal("access-failure config no longer matches the reviewed plan")
	}
	report.SourceBeforeSHA256 = migrationHash(sourceBefore)
	report.DestinationBeforeSHA256 = migrationHash(destinationBefore)
	sourceHelper := filepath.Join(embeddedRuntimePath(filepath.Dir(p.SourceExe)), "winws2.exe")
	oldDPI := waitForLifecycleDPI(t, sourceHelper, 0)
	report.DPIBefore = oldDPI.pid
	s, err := openMigrationService(report.RequestedAccess)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	limited := &limitedLifecycleMigration{scmMigrationService: scmMigrationService{s}, report: report}
	backup, err := executeServiceMigration(p, limited, nativeMigrationHooks(p))
	if backup != "" {
		backups["access-"+report.Operation] = backup
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || report.ErrorCode != uint32(windows.ERROR_ACCESS_DENIED) ||
		backup == "" || report.StopCalls != 1 {
		t.Fatalf("expected native %s denial and backup: %+v %v", report.Operation, report, err)
	}
	report.After = requireLifecycleMigrationService(t, owner, p.Previous)
	report.ServiceAfter, err = owner.Query()
	if err != nil || report.ServiceAfter.State != svc.Running || report.ServiceAfter.ProcessId == 0 {
		t.Fatalf("source unavailable after native denial: %+v %v", report.ServiceAfter, err)
	}
	if report.Operation == "stop" {
		if report.RequestedAccess&windows.SERVICE_STOP != 0 || report.BindCalls != 0 || report.StartCalls != 0 ||
			report.ServiceAfter.ProcessId != before.ProcessId {
			t.Fatalf("stop denial interrupted the source or advanced to rebind: %+v", report)
		}
	} else if report.RequestedAccess&windows.SERVICE_CHANGE_CONFIG != 0 || report.BindCalls != 1 || report.StartCalls != 1 ||
		report.StoppedBeforeBind.State != svc.Stopped || report.StoppedBeforeBind.ProcessId != 0 ||
		report.ServiceAfter.ProcessId == before.ProcessId {
		t.Fatalf("rebind denial did not exercise stopped-source rollback/restart: %+v", report)
	}
	report.TrayAfter = requireLifecycleMigrationTray(t, p.Tray)
	sourceAfter := readLifecycleMigrationFile(t, p.SourceConfig)
	destinationAfter := readLifecycleMigrationFile(t, p.TargetConfig)
	if !bytes.Equal(sourceAfter, sourceBefore) || !bytes.Equal(destinationAfter, destinationBefore) {
		t.Fatal("native access failure did not restore exact source/destination config bytes")
	}
	report.SourceAfterSHA256 = migrationHash(sourceAfter)
	report.DestinationAfterSHA256 = migrationHash(destinationAfter)
	if report.Operation == "rebind" {
		requireLifecycleDPIExited(t, oldDPI)
	}
	report.DPIAfter = waitForLifecycleDPI(t, sourceHelper, 0).pid
	if (report.DPIAfter == report.DPIBefore) != (report.Operation == "stop") {
		t.Fatalf("unexpected source DPI process lifecycle: %+v", report)
	}
	requireNoLifecycleDPI(t, filepath.Join(embeddedRuntimePath(filepath.Dir(p.TargetExe)), "winws2.exe"))
	requireLifecycleMigrationDNS(t, p.Settings.Listen)
	report.DNSVerified = true
	report.Backup = verifyLifecycleMigrationBackup(t, backup, "rolled-back", p, destinationBefore)
	report.Verified = true
	t.Logf("live migration native %s access denial verified: error 5, exact rollback, source PID and DNS verified", report.Operation)
}

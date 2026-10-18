//go:build windows && !console

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Only called by the explicitly opted-in, empty-machine lifecycle test. Unlike
// the stopped SCM fixture, this uses real SonDNS processes and production hooks.
func runLifecycleMigration(t *testing.T, built, source, sourceConfig, parent string, settings Config) {
	t.Helper()
	targetRoot := filepath.Join(parent, "SonDNS-service-test-migration-"+rand.Text())
	target := filepath.Join(targetRoot, "SonDNS.exe")
	targetConfig := filepath.Join(targetRoot, "data", "imported settings.json")
	sourceHelper := filepath.Join(embeddedRuntimePath(filepath.Dir(source)), "winws2.exe")
	targetHelper := filepath.Join(embeddedRuntimePath(targetRoot), "winws2.exe")
	report := lifecycleMigrationReport{}
	backups := map[string]string{}
	// This runs before the outer source-only cleanup. Accept only either exact
	// fixture binding, so a failed migration can be uninstalled by its owner.
	defer func() {
		defer func() {
			saveLifecycleMigrationReports(t, report, backups, source, target)
			if report.CleanupVerified && insideRoot(parent, targetRoot) &&
				strings.HasPrefix(filepath.Base(targetRoot), "SonDNS-service-test-migration-") {
				if err := os.RemoveAll(targetRoot); err != nil {
					t.Errorf("remove migration fixture: %v", err)
				}
			}
		}()
		cleanupLifecycleMigration(t, source, sourceConfig, target, targetConfig)
		if !lifecycleMigrationAbsent(t) {
			t.Logf("retaining migration fixture for recovery: %s", targetRoot)
			return
		}
		requireNoLifecycleDPI(t, sourceHelper)
		requireNoLifecycleDPI(t, targetHelper)
		report.CleanupVerified = true
		t.Log("live migration cleanup verified: service, driver, tray and both helper paths absent")
	}()
	placeholder := DefaultConfig()
	placeholder.DPI.Enabled = false
	if _, err := createSecurePortableCopy(built, targetRoot, placeholder); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(targetConfig, placeholder); err != nil {
		t.Fatal(err)
	}
	targetBefore := readLifecycleMigrationFile(t, targetConfig)
	settings = settings.Clone()
	settings.ServiceAutoStart = true
	settings.DPI.Enabled = true
	settings.DPI.CaptureMode = "tcp"
	settings.DPI.Targets = []string{"sondns-test.invalid"}
	settings.DPI.Exclude = []string{"sondns-excluded.invalid"}
	if err := SaveConfig(sourceConfig, settings); err != nil {
		t.Fatal(err)
	}
	runLifecycleMigrationCommand(t, source, "install", "--config", sourceConfig)
	s, err := openMigrationService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS |
		windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := requireServiceConfigForExe(s, source, sourceConfig); err != nil {
		t.Fatal(err)
	}
	initial, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	initial.Description = "Disposable running SonDNS migration fixture"
	initial.Dependencies = []string{"Tcpip"}
	initial.DelayedAutoStart = true
	if err := s.UpdateConfig(initial); err != nil {
		t.Fatal(err)
	}
	initial, err = s.Config()
	if err != nil || initial.Description != "Disposable running SonDNS migration fixture" ||
		!initial.DelayedAutoStart || !reflect.DeepEqual(initial.Dependencies, []string{"Tcpip"}) {
		t.Fatalf("nondefault SCM fixture fields: %+v %v", initial, err)
	}
	// Deliberately stale saved preference: migration must use the actual SCM mode.
	settings.ServiceAutoStart = false
	if err := SaveConfig(sourceConfig, settings); err != nil {
		t.Fatal(err)
	}
	sourceBefore := readLifecycleMigrationFile(t, sourceConfig)
	trayCommand, err := guiTrayCommand(source, sourceConfig)
	if err != nil {
		t.Fatal(err)
	}
	trayBefore := migrationTrayValue{Exists: true, Kind: registry.EXPAND_SZ, Value: trayCommand}
	if err := writeMigrationTray(registry.LOCAL_MACHINE, runRegistryKey, trayBefore); err != nil {
		t.Fatal(err)
	}
	prepare := func() serviceMigrationPlan {
		t.Helper()
		current, err := s.Config()
		if err != nil || !reflect.DeepEqual(current, initial) {
			t.Fatalf("source binding changed: %+v %v", current, err)
		}
		st, err := s.Query()
		if err != nil || st.State != svc.Running {
			t.Fatalf("source not running: %+v %v", st, err)
		}
		p, err := prepareServiceMigration(current, st.State, target, targetConfig)
		if err != nil {
			t.Fatal(err)
		}
		p.Tray, err = readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey)
		if err != nil || p.Tray != trayBefore {
			t.Fatalf("source tray changed: %+v %v", p.Tray, err)
		}
		if err := validateMigrationTray(p.Tray, source, target); err != nil {
			t.Fatal(err)
		}
		if !p.CanRestartSource || !p.Settings.ServiceAutoStart {
			t.Fatal("safe source and SCM autostart must survive migration preflight")
		}
		return p
	}
	oldDPI := waitForLifecycleDPI(t, sourceHelper, 0)
	p := prepare()
	report.Before, report.TrayBefore = initial, trayBefore
	report.SourceExeSHA256, report.SourceConfigSHA256 = p.SourceExeHash, p.SourceConfigHash
	report.DestinationConfigBeforeSHA256 = migrationHash(targetBefore)
	locked := &lockedLifecycleMigration{scmMigrationService: scmMigrationService{s}, target: target}
	backup, err := executeServiceMigration(p, locked, nativeMigrationHooks(p))
	if backup != "" {
		backups["rollback"] = backup
	}
	if err == nil || !locked.acquired || locked.startErr == nil || locked.startErrCode == 0 || !errors.Is(err, locked.startErr) || locked.starts != 2 {
		t.Fatalf("expected real locked-image SCM failure and old-service restart: acquired=%t starts=%d SCM=%v migration=%v",
			locked.acquired, locked.starts, locked.startErr, err)
	}
	report.SCMStartError, report.SCMStartErrorCode = locked.startErr.Error(), locked.startErrCode
	report.AfterRollback = requireLifecycleMigrationService(t, s, initial)
	report.TrayAfterRollback = requireLifecycleMigrationTray(t, trayBefore)
	if !bytes.Equal(readLifecycleMigrationFile(t, targetConfig), targetBefore) ||
		!bytes.Equal(readLifecycleMigrationFile(t, sourceConfig), sourceBefore) {
		t.Fatal("rollback did not preserve exact destination/source config bytes")
	}
	if err := validateMigrationFiles(p); err != nil {
		t.Fatal(err)
	}
	requireLifecycleDPIExited(t, oldDPI)
	restoredDPI := waitForLifecycleDPI(t, sourceHelper, oldDPI.pid)
	requireNoLifecycleDPI(t, targetHelper)
	requireLifecycleMigrationDNS(t, settings.Listen)
	report.RollbackBackup = verifyLifecycleMigrationBackup(t, backup, "rolled-back", p, targetBefore)
	report.RollbackVerified = true
	t.Logf("live migration rollback after real SCM startup failure: %s (code %d); old DNS restored", locked.startErr, locked.startErrCode)

	p = prepare()
	backup, err = migrateExistingService(p)
	if backup != "" {
		backups["complete"] = backup
	}
	if err != nil {
		t.Fatal(err)
	}
	want := cloneServiceConfig(initial)
	want.BinaryPathName = migrationCommand(p)
	report.AfterMigration = requireLifecycleMigrationService(t, s, want)
	wantTray, err := guiTrayCommand(target, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	report.TrayAfterMigration = requireLifecycleMigrationTray(t, migrationTrayValue{true, registry.SZ, wantTray})
	imported, err := LoadConfig(targetConfig)
	if err != nil || !reflect.DeepEqual(imported, p.Settings) {
		t.Fatalf("imported settings differ: %+v want %+v error %v", imported, p.Settings, err)
	}
	report.ImportedSettings = imported
	report.DestinationConfigAfterSHA256 = migrationHash(readLifecycleMigrationFile(t, targetConfig))
	if err := validateMigrationFiles(p); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readLifecycleMigrationFile(t, sourceConfig), sourceBefore) {
		t.Fatal("successful migration rewrote source settings")
	}
	requireLifecycleDPIExited(t, restoredDPI)
	newDPI := waitForLifecycleDPI(t, targetHelper, 0)
	report.SourceDPIPID, report.RestoredDPIPID, report.TargetDPIPID = oldDPI.pid, restoredDPI.pid, newDPI.pid
	requireNoLifecycleDPI(t, sourceHelper)
	requireLifecycleMigrationDNS(t, imported.Listen)
	report.CompletedBackup = verifyLifecycleMigrationBackup(t, backup, "complete", p, targetBefore)
	report.MigrationVerified = true
	t.Log("live migration completed: custom config, SCM fields, tray, backup hashes and DNS verified")
}

type lockedLifecycleMigration struct {
	scmMigrationService
	target       string
	starts       int
	acquired     bool
	startErr     error
	startErrCode uint32
}

func (s *lockedLifecycleMigration) StartAndWait() error {
	s.starts++
	if s.starts != 1 {
		return s.scmMigrationService.StartAndWait()
	}
	name, err := windows.UTF16PtrFromString(s.target)
	if err != nil {
		return err
	}
	// Lock only the candidate image during the actual SCM start. Do not corrupt
	// any files or return a synthetic failure in place of Windows' own result.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	s.acquired = true
	s.startErr = s.scmMigrationService.StartAndWait()
	var errno windows.Errno
	if errors.As(s.startErr, &errno) {
		s.startErrCode = uint32(errno)
	}
	return errors.Join(s.startErr, windows.CloseHandle(h))
}

type lifecycleMigrationBackupReport struct {
	Status         string
	FilesVerified  int
	RuntimeFiles   int
	ManifestSHA256 string
}

type lifecycleMigrationReport struct {
	Before, AfterRollback, AfterMigration                       mgr.Config
	TrayBefore, TrayAfterRollback, TrayAfterMigration           migrationTrayValue
	SourceExeSHA256, SourceConfigSHA256                         string
	DestinationConfigBeforeSHA256, DestinationConfigAfterSHA256 string
	SCMStartError                                               string
	SCMStartErrorCode                                           uint32
	SourceDPIPID, RestoredDPIPID, TargetDPIPID                  uint32
	ImportedSettings                                            Config
	RollbackBackup, CompletedBackup                             lifecycleMigrationBackupReport
	RollbackVerified, MigrationVerified, CleanupVerified        bool
}

func readLifecycleMigrationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runLifecycleMigrationCommand(t *testing.T, exe string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, exe, args...).CombinedOutput(); err != nil {
		t.Fatalf("migration fixture %v: %v %s", args, err, output)
	}
}

func requireLifecycleMigrationService(t *testing.T, s *mgr.Service, want mgr.Config) mgr.Config {
	t.Helper()
	got, err := s.Config()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("full SCM configuration changed: got %+v want %+v error %v", got, want, err)
	}
	st, err := s.Query()
	if err != nil || st.State != svc.Running {
		t.Fatalf("migration service not running: %+v %v", st, err)
	}
	return got
}

func requireLifecycleMigrationTray(t *testing.T, want migrationTrayValue) migrationTrayValue {
	t.Helper()
	got, err := readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey)
	if err != nil || got != want {
		t.Fatalf("migration tray content/type: got %+v want %+v error %v", got, want, err)
	}
	return got
}

func requireLifecycleMigrationDNS(t *testing.T, address string) {
	t.Helper()
	for _, network := range []string{"udp", "tcp"} {
		msg := new(dns.Msg)
		msg.SetQuestion("sondns-test.invalid.", dns.TypeA)
		reply, _, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).Exchange(msg, address)
		if err != nil {
			t.Fatal(err)
		}
		requireEncryptedFixtureReply(t, msg, reply)
	}
}

func verifyLifecycleMigrationBackup(t *testing.T, backup, status string, p serviceMigrationPlan, destinationBefore []byte) lifecycleMigrationBackupReport {
	t.Helper()
	journal := readLifecycleMigrationFile(t, filepath.Join(backup, "journal.json"))
	var record struct {
		Status string
		Plan   serviceMigrationPlan
	}
	if err := json.Unmarshal(journal, &record); err != nil || record.Status != status || !reflect.DeepEqual(record.Plan, p) {
		t.Fatalf("migration journal: %+v %v", record, err)
	}
	manifest := readLifecycleMigrationFile(t, filepath.Join(backup, "files.json"))
	var files []migrationBackupFile
	if err := json.Unmarshal(manifest, &files); err != nil {
		t.Fatal(err)
	}
	result := lifecycleMigrationBackupReport{Status: status, ManifestSHA256: migrationHash(manifest)}
	sawExe, sawConfig := false, false
	for _, file := range files {
		if !insideRoot(backup, filepath.Join(backup, file.Backup)) || !strings.HasSuffix(file.Backup, ".bin") ||
			!insideRoot(filepath.Dir(p.SourceExe), file.Source) {
			t.Fatalf("backup manifest path: %+v", file)
		}
		data := readLifecycleMigrationFile(t, filepath.Join(backup, file.Backup))
		if len(data) != file.Size || migrationHash(data) != file.SHA256 ||
			migrationHash(readLifecycleMigrationFile(t, file.Source)) != file.SHA256 {
			t.Fatalf("backup/source hash mismatch: %s", file.Source)
		}
		sawExe = sawExe || strings.EqualFold(file.Source, p.SourceExe) && file.SHA256 == p.SourceExeHash
		sawConfig = sawConfig || strings.EqualFold(file.Source, p.SourceConfig) && file.SHA256 == p.SourceConfigHash
		if insideRoot(filepath.Join(filepath.Dir(p.SourceExe), "runtime"), file.Source) {
			result.RuntimeFiles++
		}
		result.FilesVerified++
	}
	if !sawExe || !sawConfig || result.RuntimeFiles == 0 ||
		!bytes.Equal(readLifecycleMigrationFile(t, filepath.Join(backup, "destination-config.json.bin")), destinationBefore) ||
		!bytes.Contains(readLifecycleMigrationFile(t, filepath.Join(backup, "RECOVERY.txt")), []byte("No automatic recovery runs after a crash or power loss.")) {
		t.Fatal("backup missing source EXE, config, runtime, destination config or recovery instructions")
	}
	return result
}

func cleanupLifecycleMigration(t *testing.T, source, sourceConfig, target, targetConfig string) {
	t.Helper()
	s, err := openMigrationService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return
	}
	if err != nil {
		t.Errorf("migration cleanup SCM: %v", err)
		return
	}
	var owner string
	for _, candidate := range []struct{ exe, config string }{{source, sourceConfig}, {target, targetConfig}} {
		if requireServiceConfigForExe(s, candidate.exe, candidate.config) == nil {
			owner = candidate.exe
			break
		}
	}
	_ = s.Close()
	if owner == "" {
		t.Error("migration cleanup refused an unrelated service binding")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, owner, "uninstall").CombinedOutput(); err != nil {
		t.Errorf("uninstall owning migration fixture: %v %s", err, output)
	}
	if tray, err := readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey); err != nil || tray.Exists {
		t.Errorf("migration cleanup tray remains: %+v %v", tray, err)
	}
}

func lifecycleMigrationAbsent(t *testing.T) bool {
	t.Helper()
	snap, err := queryServiceSnapshot()
	if err != nil || snap.Exists {
		t.Errorf("migration cleanup service remains: %+v %v", snap, err)
		return false
	}
	m, err := mgr.Connect()
	if err != nil {
		t.Error(err)
		return false
	}
	defer m.Disconnect()
	driver, err := m.OpenService(winDivertServiceName)
	if err == nil {
		_ = driver.Close()
	}
	if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Errorf("migration cleanup driver may remain: %v", err)
		return false
	}
	return true
}

func saveLifecycleMigrationReports(t *testing.T, report lifecycleMigrationReport, backups map[string]string, source, target string) {
	t.Helper()
	destination := os.Getenv("SONDNS_SERVICE_TEST_LOG")
	if destination == "" {
		return
	}
	root := filepath.Dir(destination)
	save := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Errorf("save migration report %s: %v", name, err)
		}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Error(err)
	} else {
		save("migration.json", append(data, '\n'))
	}
	// Explicit report whitelist: never upload backed-up EXEs/runtime .bin files.
	for label, directory := range backups {
		for _, name := range []string{"journal.json", "files.json", "RECOVERY.txt"} {
			contents, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Errorf("read migration report: %v", err)
				continue
			}
			save("migration-"+label+"-"+name, contents)
		}
	}
	for _, item := range []struct{ name, path string }{
		{"migration-source-runtime.log", filepath.Join(filepath.Dir(source), "data", "system.log")},
		{"migration-target-runtime.log", filepath.Join(filepath.Dir(target), defaultLogPath())},
	} {
		contents, err := os.ReadFile(item.path)
		if errors.Is(err, os.ErrNotExist) {
			if report.MigrationVerified {
				t.Errorf("successful migration runtime log is missing: %s", item.path)
			}
			continue // A failed preflight may not have started this runtime.
		}
		if err != nil {
			t.Errorf("read migration runtime log: %v", err)
			continue
		}
		save(item.name, contents)
		if report.MigrationVerified && (!bytes.Contains(contents, []byte("dpi: windivert initialized. capture is started.")) ||
			!bytes.Contains(contents, []byte("service stopped successfully"))) {
			t.Errorf("migration log lacks DPI initialization or completed service stop: %s", item.path)
		}
		for _, issue := range []string{"WinDivert shutdown failed", "cancel WinDivert I/O", "close WinDivert handle",
			"WinDivert shutdown wait timed out", "WinDivert cleanup incomplete", "timed out waiting for DPI backend",
			"stop DPI backend:", "DPI backend unavailable", "DPI portable directories unavailable"} {
			if bytes.Contains(contents, []byte(issue)) {
				t.Errorf("migration runtime contains %q", issue)
			}
		}
	}
}

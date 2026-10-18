//go:build windows && !console

package service

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
	appconfig "sondns/internal/config"
	"sondns/internal/testutil"
)

// Only called by the explicitly opted-in, empty-machine lifecycle test. Unlike
// the stopped SCM fixture, this uses real SonDNS processes and production hooks.
func runLifecycleMigration(t *testing.T, built, source, sourceConfig, parent string, settings appconfig.Config) {
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
		requireLifecycleMigrationTray(t, migrationTrayValue{})
		requireNoLifecycleDPI(t, sourceHelper)
		requireNoLifecycleDPI(t, targetHelper)
		report.CleanupVerified = true
		t.Log("live migration cleanup verified: service, driver, tray and both helper paths absent")
	}()
	placeholder := appconfig.Default()
	placeholder.DPI.Enabled = false
	if _, err := CreateSecurePortableCopy(built, targetRoot, placeholder); err != nil {
		t.Fatal(err)
	}
	if err := appconfig.Save(targetConfig, placeholder); err != nil {
		t.Fatal(err)
	}
	targetBefore := readLifecycleMigrationFile(t, targetConfig)
	settings = settings.Clone()
	settings.ServiceAutoStart = true
	settings.DPI.Enabled = true
	settings.DPI.CaptureMode = "tcp"
	settings.DPI.Targets = []string{"sondns-test.invalid"}
	settings.DPI.Exclude = []string{"sondns-excluded.invalid"}
	if err := appconfig.Save(sourceConfig, settings); err != nil {
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
	if err := appconfig.Save(sourceConfig, settings); err != nil {
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
	runLifecycleGUIRejects(t, p, &report.GUI, func() {
		_ = prepare()
		requireLifecycleMigrationService(t, s, initial)
		requireLifecycleMigrationTray(t, trayBefore)
		if !bytes.Equal(readLifecycleMigrationFile(t, targetConfig), targetBefore) ||
			!bytes.Equal(readLifecycleMigrationFile(t, sourceConfig), sourceBefore) {
			t.Fatal("GUI cancel/validation failure changed source or destination config bytes")
		}
		currentDPI := waitForLifecycleDPI(t, sourceHelper, 0)
		if currentDPI.pid != oldDPI.pid {
			t.Fatal("GUI cancel/validation failure restarted the old DPI process")
		}
		requireNoLifecycleDPI(t, targetHelper)
		requireLifecycleMigrationDNS(t, settings.Listen)
	})
	runLifecycleMigrationAccessFailures(t, s, p, targetBefore, backups, &report.AccessFailures)
	runLifecycleMigrationInterruptions(t, s, p, targetBefore, parent, backups, &report.Interruptions)
	oldDPI = waitForLifecycleDPI(t, sourceHelper, 0)
	p = prepare()
	locked := &lockedLifecycleMigration{scmMigrationService: scmMigrationService{s}, target: target}
	defer func() {
		if err := locked.release(); err != nil {
			t.Errorf("release owned candidate image lock: %v", err)
		}
	}()
	hooks := nativeMigrationHooks(p)
	validateSource := hooks.ValidateSource
	hooks.ValidateSource = func() error {
		// Demonstrate the old full validation is still blocked by the candidate,
		// then exercise the product's source-only restart validation.
		candidateErr := validateMigrationFiles(p)
		if !errors.Is(candidateErr, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("candidate must remain unreadable during rollback validation: %v", candidateErr)
		}
		report.CandidateValidationError, report.CandidateValidationErrorCode = candidateErr.Error(), uint32(windows.ERROR_SHARING_VIOLATION)
		err := validateSource()
		report.SourceRestartValidationPassed = err == nil
		return err
	}
	backup, err := executeServiceMigration(p, locked, hooks)
	if backup != "" {
		backups["rollback"] = backup
	}
	if err == nil || !locked.acquired || locked.startErr == nil || locked.startErrCode == 0 || !errors.Is(err, locked.startErr) || locked.starts != 2 || !locked.sourceRestartWithLock {
		t.Fatalf("expected real locked-image SCM failure and old-service restart: acquired=%t starts=%d SCM=%v migration=%v",
			locked.acquired, locked.starts, locked.startErr, err)
	}
	report.CandidateLockHeldDuringRestart = locked.sourceRestartWithLock
	report.SCMStartError, report.SCMStartErrorCode = locked.startErr.Error(), locked.startErrCode
	report.AfterRollback = requireLifecycleMigrationService(t, s, initial)
	report.TrayAfterRollback = requireLifecycleMigrationTray(t, trayBefore)
	if !bytes.Equal(readLifecycleMigrationFile(t, targetConfig), targetBefore) ||
		!bytes.Equal(readLifecycleMigrationFile(t, sourceConfig), sourceBefore) {
		t.Fatal("rollback did not preserve exact destination/source config bytes")
	}
	if err := validateMigrationSourceForRestart(p); err != nil {
		t.Fatal(err)
	}
	requireLifecycleDPIExited(t, oldDPI)
	restoredDPI := waitForLifecycleDPI(t, sourceHelper, oldDPI.pid)
	requireNoLifecycleDPI(t, targetHelper)
	requireLifecycleMigrationDNS(t, settings.Listen)
	if !errors.Is(validateMigrationFiles(p), windows.ERROR_SHARING_VIOLATION) || locked.lock == 0 {
		t.Fatal("candidate image lock did not survive through old-service DNS recovery")
	}
	report.DNSRecoveredWithCandidateLocked = true
	report.RollbackBackup = verifyLifecycleMigrationBackup(t, backup, "rolled-back", p, targetBefore)
	if err := locked.release(); err != nil {
		t.Fatal(err)
	}
	report.CandidateLockReleased = true
	if err := validateMigrationFiles(p); err != nil {
		t.Fatal(err)
	}
	report.RollbackVerified = true
	t.Logf("live migration rollback after real SCM startup failure: %s (code %d); old DNS restored", locked.startErr, locked.startErrCode)
	t.Log("live migration rollback kept candidate image locked through old-service DNS recovery")

	p = prepare()
	backup = runLifecycleGUIMigrate(t, p, &report.GUI)
	if backup != "" {
		backups["complete"] = backup
	}
	want := cloneServiceConfig(initial)
	want.BinaryPathName = migrationCommand(p)
	report.AfterMigration = requireLifecycleMigrationService(t, s, want)
	wantTray, err := guiTrayCommand(target, targetConfig)
	if err != nil {
		t.Fatal(err)
	}
	report.TrayAfterMigration = requireLifecycleMigrationTray(t, migrationTrayValue{true, registry.SZ, wantTray})
	imported, err := appconfig.Load(targetConfig)
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
	target                string
	starts                int
	acquired              bool
	startErr              error
	startErrCode          uint32
	lock                  windows.Handle
	sourceRestartWithLock bool
}

func (s *lockedLifecycleMigration) release() error {
	if s.lock == 0 {
		return nil
	}
	err := windows.CloseHandle(s.lock)
	if err == nil {
		s.lock = 0
	}
	return err
}

func (s *lockedLifecycleMigration) StartAndWait() error {
	s.starts++
	if s.starts != 1 {
		s.sourceRestartWithLock = s.lock != 0
		return s.scmMigrationService.StartAndWait()
	}
	name, err := windows.UTF16PtrFromString(s.target)
	if err != nil {
		return err
	}
	// Keep only the candidate image locked through the actual SCM failure,
	// rollback, old-service restart and DNS probe. Never corrupt any file or
	// substitute a synthetic error for Windows' result.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	s.lock = h
	s.acquired = true
	s.startErr = s.scmMigrationService.StartAndWait()
	var errno windows.Errno
	if errors.As(s.startErr, &errno) {
		s.startErrCode = uint32(errno)
	}
	return s.startErr
}

type lifecycleMigrationBackupReport struct {
	Status         string
	FilesVerified  int
	RuntimeFiles   int
	ManifestSHA256 string
	SchemaVersion  int
	Recovery       migrationRecoveryMetadata
}

type lifecycleMigrationReport struct {
	Before, AfterRollback, AfterMigration                         mgr.Config
	TrayBefore, TrayAfterRollback, TrayAfterMigration             migrationTrayValue
	SourceExeSHA256, SourceConfigSHA256                           string
	DestinationConfigBeforeSHA256, DestinationConfigAfterSHA256   string
	SCMStartError                                                 string
	SCMStartErrorCode                                             uint32
	SourceDPIPID, RestoredDPIPID, TargetDPIPID                    uint32
	ImportedSettings                                              appconfig.Config
	RollbackBackup, CompletedBackup                               lifecycleMigrationBackupReport
	RollbackVerified, MigrationVerified, CleanupVerified          bool
	GUI                                                           lifecycleMigrationGUIReport
	Interruptions                                                 []lifecycleMigrationInterruptionReport
	AccessFailures                                                []lifecycleMigrationAccessReport
	CandidateValidationError                                      string
	CandidateValidationErrorCode                                  uint32
	SourceRestartValidationPassed, CandidateLockHeldDuringRestart bool
	DNSRecoveredWithCandidateLocked, CandidateLockReleased        bool
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
		testutil.RequireEncryptedFixtureReply(t, msg, reply)
	}
}

func verifyLifecycleMigrationBackup(t *testing.T, backup, status string, p serviceMigrationPlan, destinationBefore []byte) lifecycleMigrationBackupReport {
	t.Helper()
	journal := readLifecycleMigrationFile(t, filepath.Join(backup, "journal.json"))
	var record migrationJournal
	if err := json.Unmarshal(journal, &record); err != nil || record.Status != status || !reflect.DeepEqual(record.Plan, p) {
		t.Fatalf("migration journal: %+v %v", record, err)
	}
	manifest := readLifecycleMigrationFile(t, filepath.Join(backup, "files.json"))
	var files []migrationBackupFile
	if err := json.Unmarshal(manifest, &files); err != nil {
		t.Fatal(err)
	}
	candidate, err := json.MarshalIndent(p.Settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	candidate = append(candidate, '\n')
	if record.SchemaVersion != 1 || !record.Recovery.Ready ||
		record.Recovery.FilesManifestSHA256 != migrationHash(manifest) || !record.Recovery.DestinationExisted ||
		record.Recovery.DestinationSHA256 != migrationHash(destinationBefore) || record.Recovery.DestinationSize != len(destinationBefore) ||
		record.Recovery.CandidateSHA256 != migrationHash(candidate) || record.Recovery.CandidateSize != len(candidate) {
		t.Fatalf("migration recovery metadata does not describe the actual backup/candidate bytes: %+v", record.Recovery)
	}
	result := lifecycleMigrationBackupReport{Status: status, ManifestSHA256: migrationHash(manifest), SchemaVersion: record.SchemaVersion, Recovery: record.Recovery}
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
	snap, err := QueryServiceSnapshot()
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
		{"migration-target-runtime.log", filepath.Join(filepath.Dir(target), appconfig.DefaultLogPath())},
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

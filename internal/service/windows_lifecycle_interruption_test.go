//go:build windows && !console

package service

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

var lifecycleInterruptPlan = flag.String("sondns-interrupt-plan", "", "owned migration interruption fixture plan")

var lifecycleInterruptPoint = flag.String("sondns-interrupt-point", "", "owned migration interruption boundary")

var lifecycleInterruptReady = flag.String("sondns-interrupt-ready", "", "owned migration interruption barrier")

const lifecycleInterruptExitCode = 173

type lifecycleMigrationInterruptionReport struct {
	Point, JournalStatus, JournalSHA256, DestinationSHA256 string
	Stage                                                  string
	ChildPID, ChildExitCode, ServiceState                  uint32
	Binding                                                mgr.Config
	Tray                                                   migrationTrayValue
	Backup                                                 lifecycleMigrationBackupReport
	ChildTerminated, GUIUnchanged, ManualRestoreVerified   bool
	DNSAvailable, DNSUnavailable                           bool
	GUIProcessesReaped                                     int
}

type lifecycleInterruptionBarrier struct {
	Point, Backup string
	PID           uint32
}

// The barriers wrap existing migration interfaces only in this test harness.
// Product code has no environment-triggered interruption or recovery hooks.
func TestLifecycleMigrationInterruptChild(t *testing.T) {
	if *lifecycleInterruptPlan == "" {
		t.Skip("owned disposable migration interruption child")
	}
	if os.Getenv("SONDNS_SERVICE_TEST") != "1" || os.Getenv("SONDNS_ACCOUNT_TEST") != "1" ||
		*lifecycleInterruptReady == "" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("migration interruption requires the opted-in elevated disposable fixture")
	}
	switch *lifecycleInterruptPoint {
	case "prepared", "switching", "after-stop", "bound", "after-start", "after-tray":
	default:
		t.Fatal("unknown migration interruption boundary")
	}
	var p serviceMigrationPlan
	if err := json.Unmarshal(readLifecycleMigrationFile(t, *lifecycleInterruptPlan), &p); err != nil {
		t.Fatal(err)
	}
	s, err := openMigrationService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS |
		windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START | windows.SERVICE_STOP)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() // The parent deliberately terminates us before this runs.
	var directory string
	barrier := func(point string) {
		if point != *lifecycleInterruptPoint {
			return
		}
		data, err := json.Marshal(lifecycleInterruptionBarrier{point, directory, uint32(os.Getpid())})
		if err == nil {
			err = atomicMigrationWrite(*lifecycleInterruptReady, data)
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		t.Fatal("parent did not terminate the owned interruption child")
	}
	hooks := nativeMigrationHooks(p)
	stage := hooks.Stage
	hooks.Stage = func() (migrationStage, error) {
		result, err := stage()
		if err != nil {
			return result, err
		}
		directory = result.Directory
		barrier("prepared")
		journal := result.Journal
		result.Journal = func(status string, cause error) error {
			if err := journal(status, cause); err != nil {
				return err
			}
			barrier(status)
			return nil
		}
		return result, nil
	}
	tray := hooks.ChangeTray
	hooks.ChangeTray = func() (func() error, error) {
		undo, err := tray()
		if err == nil {
			barrier("after-tray")
		}
		return undo, err
	}
	_, err = executeServiceMigration(p, lifecycleInterruptedService{scmMigrationService{s}, barrier}, hooks)
	t.Fatalf("migration returned instead of reaching %q: %v", *lifecycleInterruptPoint, err)
}

type lifecycleInterruptedService struct {
	scmMigrationService
	barrier func(string)
}

func (s lifecycleInterruptedService) StopAndWait() error {
	if err := s.scmMigrationService.StopAndWait(); err != nil {
		return err
	}
	s.barrier("after-stop")
	return nil
}

func (s lifecycleInterruptedService) StartAndWait() error {
	if err := s.scmMigrationService.StartAndWait(); err != nil {
		return err
	}
	s.barrier("after-start")
	return nil
}

func runLifecycleMigrationInterruptions(t *testing.T, s *mgr.Service, p serviceMigrationPlan, original []byte, parent string, backups map[string]string, reports *[]lifecycleMigrationInterruptionReport) {
	t.Helper()
	for _, point := range []string{"prepared", "switching", "after-stop", "bound", "after-start", "after-tray"} {
		*reports = append(*reports, lifecycleMigrationInterruptionReport{Point: point, Stage: "launch-child"})
		report := &(*reports)[len(*reports)-1]
		backup := killLifecycleMigrationChild(t, p, point, parent, report)
		backups["interrupted-"+point] = backup
		report.Stage = "inspect-backup"
		status := "bound"
		if point == "prepared" || point == "switching" {
			status = point
		} else if point == "after-stop" {
			status = "switching"
		}
		report.JournalStatus = status
		report.Backup = verifyLifecycleMigrationBackup(t, backup, status, p, original)
		report.JournalSHA256 = migrationHash(readLifecycleMigrationFile(t, filepath.Join(backup, "journal.json")))
		report.DestinationSHA256 = migrationHash(readLifecycleMigrationFile(t, p.TargetConfig))
		if report.DestinationSHA256 != report.Backup.Recovery.CandidateSHA256 {
			t.Fatal("interrupted migration lost its journaled candidate config")
		}
		want := cloneServiceConfig(p.Previous)
		if point == "bound" || point == "after-start" || point == "after-tray" {
			want.BinaryPathName = migrationCommand(p)
		}
		report.ServiceState = uint32(svc.Running)
		if point == "after-stop" || point == "bound" {
			report.ServiceState = uint32(svc.Stopped)
		}
		wantTray := p.Tray
		if point == "after-tray" {
			command, err := guiTrayCommand(p.TargetExe, p.TargetConfig)
			if err != nil {
				t.Fatal(err)
			}
			wantTray = migrationTrayValue{true, registry.SZ, command}
		}
		unchanged := func() {
			t.Helper()
			current, err := s.Config()
			state, stateErr := s.Query()
			if err != nil || stateErr != nil || !reflect.DeepEqual(current, want) || uint32(state.State) != report.ServiceState {
				t.Fatalf("interrupted %s SCM changed: config=%+v state=%+v errors=%v/%v", point, current, state, err, stateErr)
			}
			report.Binding = current
			report.Tray = requireLifecycleMigrationTray(t, wantTray)
			if migrationHash(readLifecycleMigrationFile(t, p.SourceConfig)) != p.SourceConfigHash ||
				migrationHash(readLifecycleMigrationFile(t, p.TargetConfig)) != report.DestinationSHA256 ||
				migrationHash(readLifecycleMigrationFile(t, filepath.Join(backup, "journal.json"))) != report.JournalSHA256 {
				t.Fatal("interrupted migration source/config/journal changed unexpectedly")
			}
		}
		unchanged()
		if report.ServiceState == uint32(svc.Running) {
			requireLifecycleMigrationDNS(t, p.Settings.Listen)
			report.DNSAvailable = true
		} else {
			for _, network := range []string{"udp", "tcp"} {
				q := new(dns.Msg)
				q.SetQuestion("sondns-test.invalid.", dns.TypeA)
				if _, _, err := (&dns.Client{Net: network, Timeout: 250 * time.Millisecond}).Exchange(q, p.Settings.Listen); err == nil {
					t.Fatal("stopped interrupted service unexpectedly answered local DNS")
				}
			}
			report.DNSUnavailable = true
		}
		var guiReport lifecycleMigrationGUIReport
		report.Stage = "restart-gui"
		gui := startLifecycleGUI(t, p.TargetExe, p.TargetConfig, &guiReport)
		func() {
			defer gui.stop()
			time.Sleep(time.Second)
			unchanged() // Actual GUI startup must not replay the interrupted journal.
		}()
		unchanged()
		report.GUIProcessesReaped, report.GUIUnchanged = guiReport.ProcessesReaped, true
		report.Stage = "manual-restore"
		// Explicit operator-style restoration using verified journal/backup facts.
		// This is test code, never a startup recovery path in the application.
		native := scmMigrationService{s}
		if want.BinaryPathName != p.Previous.BinaryPathName {
			if err := native.StopAndWait(); err != nil {
				t.Fatal(err)
			}
			if err := checkMigrationBinding(native, want); err != nil {
				t.Fatal(err)
			}
			if err := native.SetBinding(p.Previous.BinaryPathName); err != nil {
				t.Fatal(err)
			}
		}
		before := readLifecycleMigrationFile(t, filepath.Join(backup, "destination-config.json.bin"))
		if err := migrationConfigUndo(filepath.Dir(p.TargetExe), p.TargetConfig, before, true, report.Backup.Recovery.CandidateSHA256)(); err != nil {
			t.Fatal(err)
		}
		if wantTray != p.Tray {
			requireLifecycleMigrationTray(t, wantTray)
			if err := writeMigrationTray(registry.LOCAL_MACHINE, runRegistryKey, p.Tray); err != nil {
				t.Fatal(err)
			}
		}
		if err := ValidatePortableServiceFolder(filepath.Dir(p.SourceExe)); err != nil {
			t.Fatal(err)
		}
		if err := validateMigrationFiles(p); err != nil {
			t.Fatal(err)
		}
		if err := checkMigrationBinding(native, p.Previous); err != nil {
			t.Fatal(err)
		}
		if err := native.StartAndWait(); err != nil {
			t.Fatal(err)
		}
		requireLifecycleMigrationService(t, s, p.Previous)
		requireLifecycleMigrationTray(t, p.Tray)
		if !bytes.Equal(original, readLifecycleMigrationFile(t, p.TargetConfig)) ||
			migrationHash(readLifecycleMigrationFile(t, p.SourceConfig)) != p.SourceConfigHash ||
			migrationHash(readLifecycleMigrationFile(t, filepath.Join(backup, "journal.json"))) != report.JournalSHA256 {
			t.Fatal("explicit manual restoration did not preserve journal/source/exact destination bytes")
		}
		requireLifecycleMigrationDNS(t, p.Settings.Listen)
		report.ManualRestoreVerified = true
		report.Stage = "completed"
		t.Logf("live interrupted migration verified: %s; child terminated, GUI unchanged, manual restore and DNS verified", point)
	}
}

func killLifecycleMigrationChild(t *testing.T, p serviceMigrationPlan, point, parent string, report *lifecycleMigrationInterruptionReport) string {
	t.Helper()
	directory := filepath.Join(parent, "SonDNS-interruption-control-"+rand.Text())
	if err := CreateServiceDirectory(directory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if insideRoot(parent, directory) && strings.HasPrefix(filepath.Base(directory), "SonDNS-interruption-control-") {
			if err := os.RemoveAll(directory); err != nil {
				t.Errorf("remove owned interruption control directory: %v", err)
			}
		}
	}()
	plan, ready := filepath.Join(directory, "plan.json"), filepath.Join(directory, "ready.json")
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, data, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "child.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(exe, "-test.run=^TestLifecycleMigrationInterruptChild$", "-test.timeout=3m",
		"-sondns-interrupt-plan="+plan, "-sondns-interrupt-point="+point, "-sondns-interrupt-ready="+ready)
	windowsRoot, err := windows.GetWindowsDirectory()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = []string{"SONDNS_SERVICE_TEST=1", "SONDNS_ACCOUNT_TEST=1", "SystemRoot=" + windowsRoot, "WINDIR=" + windowsRoot}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	report.ChildPID = uint32(cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	reaped := false
	defer func() {
		if !reaped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	var barrier lifecycleInterruptionBarrier
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(ready)
		if err == nil {
			if err := json.Unmarshal(data, &barrier); err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			reaped = true
			t.Fatalf("interruption child exited before %s: %v: %s", point, err, readLifecycleMigrationFile(t, logPath))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("interruption child did not reach %s: %s", point, readLifecycleMigrationFile(t, logPath))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if barrier.Point != point || barrier.PID != uint32(cmd.Process.Pid) ||
		!strings.EqualFold(filepath.Dir(barrier.Backup), filepath.Join(filepath.Dir(p.TargetExe), "backups")) ||
		!strings.HasPrefix(filepath.Base(barrier.Backup), "migration-") {
		t.Fatalf("interruption barrier is not owned by this child/target: %+v", barrier)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, barrier.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, lifecycleInterruptExitCode); err != nil {
		t.Fatal(err)
	}
	waitErr := <-done
	reaped = true
	if waitErr == nil {
		t.Fatal("interrupted child exited without the forced native error")
	}
	if err := windows.GetExitCodeProcess(h, &report.ChildExitCode); err != nil || report.ChildExitCode != lifecycleInterruptExitCode {
		t.Fatalf("interrupted child exit code=%d error=%v", report.ChildExitCode, err)
	}
	report.ChildPID, report.ChildTerminated = barrier.PID, true
	return barrier.Backup
}

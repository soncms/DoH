//go:build windows && console

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
)

func ensureConsoleConfig(root string) error {
	p := filepath.Join(root, "data", "sondns.json")
	if _, err := os.Stat(p); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	c := appconfig.Default()
	c.LogFile = filepath.Join("data", "sondns.log")
	return appconfig.Save(p, c)
}

func consoleConfig(root string) (appconfig.Config, error) {
	if err := ensureConsoleConfig(root); err != nil {
		return appconfig.Config{}, err
	}
	return appconfig.Load(filepath.Join(root, "data", "sondns.json"))
}

func consoleService(root string) (mgr.Config, svc.Status, bool, error) {
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return mgr.Config{}, svc.Status{}, false, nil
	}
	if err != nil {
		return mgr.Config{}, svc.Status{}, false, err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return c, svc.Status{}, false, err
	}
	st, err := s.Query()
	own := strings.EqualFold(filepath.Clean(serviceBinaryPath(c.BinaryPathName)), filepath.Join(root, "SonDNS.exe"))
	return c, st, own, err
}

func requireOwnService(root string) error {
	c, _, own, err := consoleService(root)
	if err != nil {
		return err
	}
	if !own {
		return fmt.Errorf("service is not bound to this folder (current: %s); use Service > Install/bind", c.BinaryPathName)
	}
	return nil
}

func waitRunning(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := getServiceStatus()
		if err != nil {
			return err
		}
		if st.State == svc.Running {
			return nil
		}
		if st.State == svc.Stopped {
			return errors.New("service stopped during startup; inspect data/sondns.log")
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("timed out waiting for running service")
}

func removeTrayAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue("SonDNS")
	if errors.Is(err, registry.ErrNotExist) {
		err = nil
	}
	return err
}

func backupConsoleState(root, label string) (string, error) {
	dir := filepath.Join(root, "backups", time.Now().UTC().Format("20060102T150405.000000000")+"-"+label)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, name := range []string{"sondns.json", "components.lock.json"} {
		b, err := os.ReadFile(filepath.Join(root, "data", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err = writeAtomic(filepath.Join(dir, name), b); err != nil {
			return "", err
		}
	}
	c, st, _, err := consoleService(root)
	if err != nil {
		return "", err
	}
	err = writeJSONAtomic(filepath.Join(dir, "service.json"), struct {
		Config mgr.Config
		Status svc.Status
	}{c, st})
	return dir, err
}

func bindConsoleService(root string) error {
	if err := ensureConsoleConfig(root); err != nil {
		return err
	}
	lock, err := loadComponents(root)
	if err != nil {
		return err
	}
	if err = verifyInstalled(root, lock); err != nil {
		return err
	}
	if err = protectConsoleInstallation(root); err != nil {
		return err
	}
	c, err := consoleConfig(root)
	if err != nil {
		return err
	}
	if err = validateConsoleCandidate(context.Background(), root, lock, c); err != nil {
		return fmt.Errorf("DNS preflight failed; existing service unchanged: %w", err)
	}
	backup, err := backupConsoleState(root, "service-bind")
	if err != nil {
		return err
	}
	fmt.Println("Backup:", backup)
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		s, err = m.CreateService(Name, filepath.Join(root, "SonDNS.exe"), mgr.Config{DisplayName: "SonDNS", Description: "SonDNS console-managed encrypted DNS and DPI service", StartType: serviceStartType(c.ServiceAutoStart), ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, Dependencies: []string{"Tcpip", "Dnscache"}}, "run", "--config="+filepath.Join(root, "data", "sondns.json"))
		if err != nil {
			return err
		}
		defer s.Close()
		if err = s.Start(); err != nil {
			_ = s.Delete()
			return err
		}
		if err = waitRunning(50 * time.Second); err != nil {
			_, _ = s.Control(svc.Stop)
			_ = waitForServiceStopped(s, 15*time.Second)
			_ = s.Delete()
			return err
		}
		return removeTrayAutostart()
	}
	if err != nil {
		return err
	}
	defer s.Close()
	old, err := s.Config()
	if err != nil {
		return err
	}
	st, err := s.Query()
	if err != nil {
		return err
	}
	wasRunning := st.State != svc.Stopped
	if wasRunning {
		if _, err = s.Control(svc.Stop); err != nil {
			return err
		}
		if err = waitForServiceStopped(s, 20*time.Second); err != nil {
			return err
		}
	}
	updated := old
	updated.BinaryPathName = `"` + filepath.Join(root, "SonDNS.exe") + `" run --config="` + filepath.Join(root, "data", "sondns.json") + `"`
	updated.StartType = serviceStartType(c.ServiceAutoStart)
	if err = s.UpdateConfig(updated); err == nil {
		err = s.Start()
	}
	if err == nil {
		err = waitRunning(50 * time.Second)
	}
	if err != nil {
		_, _ = s.Control(svc.Stop)
		_ = waitForServiceStopped(s, 20*time.Second)
		rollbackErr := s.UpdateConfig(old)
		if wasRunning && rollbackErr == nil {
			rollbackErr = s.Start()
		}
		return errors.Join(err, rollbackErr)
	}
	return removeTrayAutostart()
}

func unbindConsoleService(root string) error {
	if err := requireOwnService(root); err != nil {
		return err
	}
	if _, err := backupConsoleState(root, "service-remove"); err != nil {
		return err
	}
	if err := StopService(); err != nil {
		return err
	}
	s, err := openSonDNSService(windows.DELETE)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Delete()
}

func withConsoleMutation(root string, fn func() error) error {
	mutex, ok, err := AcquireProcessMutex(`Global\SonDNS.ConsoleMutation.` + installationID(root))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("another console is changing this installation; retry after it finishes")
	}
	defer mutex.Close()
	return fn()
}

func editConsoleConfig(root string, change func(*appconfig.Config) error, restart bool, out io.Writer) error {
	return withConsoleMutation(root, func() error {
		before, err := consoleConfig(root)
		if err != nil {
			return err
		}
		after := before.Clone()
		if err = change(&after); err != nil {
			return err
		}
		if err = after.Normalize(); err != nil {
			return err
		}
		_, st, own, err := consoleService(root)
		if err != nil {
			return err
		}
		if err = appconfig.Save(filepath.Join(root, "data", "sondns.json"), after); err != nil {
			return err
		}
		if !own || st.State == svc.Stopped {
			fmt.Fprintln(out, "Saved for this folder's next service start.")
			return nil
		}
		if restart {
			err = RestartService()
			if err == nil {
				err = waitRunning(50 * time.Second)
			}
		} else {
			_, err = control.CallAt(consolePipe(root, false), control.Request{Op: "set_upstream", Upstreams: after.Upstreams})
		}
		if err == nil {
			fmt.Fprintln(out, "Saved and applied.")
			return nil
		}
		var rejection *control.ResponseError
		if !restart && !errors.As(err, &rejection) {
			return fmt.Errorf("saved; live application unconfirmed: %w", err)
		}
		rollbackErr := appconfig.Save(filepath.Join(root, "data", "sondns.json"), before)
		if restart && rollbackErr == nil {
			rollbackErr = RestartService()
			if rollbackErr == nil {
				rollbackErr = waitRunning(50 * time.Second)
			}
		}
		return fmt.Errorf("change failed and previous configuration restored: %w", errors.Join(err, rollbackErr))
	})
}

func applyComponents(ctx context.Context, root string, candidate componentLock, out io.Writer) error {
	if err := verifyInstalled(root, candidate); err != nil {
		return err
	}
	preview, acquired, err := AcquireProcessMutex(`Local\SonDNS.Preview.` + installationID(root))
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("stop preview before updating components")
	}
	defer preview.Close()
	_, st, own, err := consoleService(root)
	if err != nil {
		return err
	}
	if _, pipeErr := control.CallAt(consolePipe(root, false), control.Request{Op: "state"}); pipeErr == nil && (!own || st.State != svc.Running) {
		return errors.New("stop the foreground runtime before updating")
	}
	c, err := consoleConfig(root)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Checking candidate dnsproxy on an isolated loopback port...")
	if err = validateConsoleCandidate(ctx, root, candidate, c); err != nil {
		return fmt.Errorf("candidate rejected; active components unchanged: %w", err)
	}
	backup, err := backupConsoleState(root, "components")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Backup:", backup)
	old, oldErr := loadComponents(root)
	wasRunning := own && st.State == svc.Running
	if wasRunning {
		fmt.Fprintln(out, "Stopping this folder's service...")
		if err = StopService(); err != nil {
			// stopService may stop SCM successfully but refuse a busy driver.
			// No lock has changed, so restore the previous running service.
			st, statusErr := getServiceStatus()
			if statusErr == nil && st.State == svc.Stopped {
				startErr := StartInstalledService()
				if startErr == nil {
					startErr = waitRunning(50 * time.Second)
				}
				return errors.Join(err, startErr)
			}
			return errors.Join(err, statusErr)
		}
	}
	if oldErr == nil {
		err = writeJSONAtomic(filepath.Join(root, "data", "components.previous.json"), old)
	}
	if err == nil {
		err = writeJSONAtomic(lockPath(root), candidate)
	}
	if err == nil && wasRunning {
		err = StartInstalledService()
		if err == nil {
			err = waitRunning(50 * time.Second)
		}
		if err == nil {
			_, err = control.CallAt(consolePipe(root, false), control.Request{Op: "state"})
		}
		if err == nil {
			err = probeConsoleDNS(ctx, c.Listen)
		}
	}
	if err != nil {
		if wasRunning {
			_ = StopService()
		}
		var rollbackErr error
		if oldErr == nil {
			rollbackErr = writeJSONAtomic(lockPath(root), old)
		}
		if wasRunning && rollbackErr == nil {
			rollbackErr = StartInstalledService()
			if rollbackErr == nil {
				rollbackErr = waitRunning(50 * time.Second)
			}
		}
		return fmt.Errorf("update failed; previous runtime restored: %w", errors.Join(err, rollbackErr))
	}
	fmt.Fprintln(out, "Components verified and activated.")
	return nil
}

func updateComponents(ctx context.Context, root string, out io.Writer) error {
	return withConsoleMutation(root, func() error {
		v, err := checkComponents(ctx, root, out)
		if err != nil {
			return err
		}
		current, readErr := loadComponents(root)
		if readErr == nil && current.Components["dnsproxy"].Version == v.DNS.Tag && current.Components["windivert"].Version == v.WD.Tag && current.Components["zapret2"].Version == v.Bundle {
			if err = verifyInstalled(root, current); err == nil {
				fmt.Fprintln(out, "All components are current; hashes verified.")
				return nil
			}
			fmt.Fprintln(out, "Runtime verification failed; downloading clean upstream copies.")
		}
		candidate, err := prepareComponents(ctx, root, v, out)
		if err != nil {
			return err
		}
		return applyComponents(ctx, root, candidate, out)
	})
}

// The service runs as SYSTEM. Protect the folder before binding so ordinary
// users cannot replace its executable, configuration or DLLs.
func protectConsoleInstallation(root string) error {
	if err := rejectReparseTree(root); err != nil {
		return err
	}
	cmd := exec.Command("icacls.exe", root, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F", "*S-1-5-32-545:(OI)(CI)RX")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("protect installation: %w: %s", err, b)
	}
	cmd = exec.Command("icacls.exe", filepath.Join(root, "*"), "/reset", "/T", "/Q")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("protect installation files: %w: %s", err, b)
	}
	return nil
}

func rollbackComponents(ctx context.Context, root string, out io.Writer) error {
	return withConsoleMutation(root, func() error {
		var previous componentLock
		if err := readJSONFile(filepath.Join(root, "data", "components.previous.json"), &previous); err != nil {
			return err
		}
		return applyComponents(ctx, root, previous, out)
	})
}

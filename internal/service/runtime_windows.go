package service

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	appconfig "sondns/internal/config"
)

const Name = "SonDNS"

func DefaultConfigPath() string {
	return filepath.Join(PortableRoot(), "data", "sondns.json")
}

func RunForeground(path string) error {
	path, err := PortableConfigPath(PortableRoot(), path)
	if err != nil {
		return err
	}
	c, err := appconfig.Load(path)
	if err != nil {
		return err
	}
	logger, closeLog, err := newLogger(c)
	if err != nil {
		return err
	}
	defer closeLog()
	shutdown, err := startRuntime(c, logger)
	if err != nil {
		return err
	}
	defer func() {
		shutdown()
		if err := cleanupWinDivertDriver(); err != nil {
			logger.Printf("WinDivert cleanup incomplete: %v", err)
		}
	}()
	logger.Printf("SonDNS running on %s (interception=%s)", c.Listen, c.Interception)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}

func startRuntime(c appconfig.Config, logger *log.Logger) (func(), error) {
	instance, acquired, err := AcquireProcessMutex("Global\\SonDNS.Runtime")
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, errors.New("another SonDNS runtime process is already running")
	}
	engine, resolver, err := newRuntimeEngine(c, logger)
	if err != nil {
		instance.Close()
		return nil, err
	}
	if err := engine.Start(context.Background()); err != nil {
		instance.Close()
		return nil, err
	}
	var interceptor *Interceptor
	if c.Interception == "windivert" {
		interceptor, err = NewInterceptor(c, resolver, logger)
		if err != nil {
			_ = engine.Shutdown(context.Background())
			instance.Close()
			return nil, err
		}
		if err := interceptor.Start(); err != nil {
			interceptor.Shutdown()
			_ = engine.Shutdown(context.Background())
			instance.Close()
			return nil, err
		}
	}
	dpi, err := NewDPIBackend(c, logger)
	if err != nil {
		if interceptor != nil {
			interceptor.Shutdown()
		}
		_ = engine.Shutdown(context.Background())
		instance.Close()
		return nil, err
	}
	if dpi != nil {
		if err := dpi.Start(); err != nil {
			dpi.Shutdown()
			if interceptor != nil {
				interceptor.Shutdown()
			}
			_ = engine.Shutdown(context.Background())
			instance.Close()
			return nil, err
		}
	}
	return func() {
		if dpi != nil {
			dpi.Shutdown()
		}
		if interceptor != nil {
			interceptor.Shutdown()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = engine.Shutdown(ctx)
		instance.Close()
	}, nil
}

func newLogger(c appconfig.Config) (*log.Logger, func(), error) {
	return newLoggerAt(PortableRoot(), c)
}

func newLoggerAt(root string, c appconfig.Config) (*log.Logger, func(), error) {
	if c.LogFile == "" {
		return log.New(os.Stderr, "sondns: ", log.LstdFlags), func() {}, nil
	}
	c.LogFile = filepath.Join(root, appconfig.LogPath(c.LogFile))
	if err := ensurePortableDirectory(root, filepath.Dir(c.LogFile)); err != nil {
		return nil, nil, err
	}
	if err := rejectPortableReparse(c.LogFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	f, err := os.OpenFile(c.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, err
	}
	return log.New(f, "sondns: ", log.LstdFlags), func() { _ = f.Close() }, nil
}

type windowsService struct{ configPath string }

func (d *windowsService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 15_000, CheckPoint: 1}
	if !ConsoleBuild {
		if err := ValidatePortableServiceFolder(PortableRoot()); err != nil {
			return true, 2
		}
	}
	c, err := appconfig.Load(d.configPath)
	if err != nil {
		return true, 1
	}
	logger, closeLog, err := newLogger(c)
	if err != nil {
		return true, 1
	}
	defer closeLog()

	var shutdown func()
	checkpoint := uint32(1)
	deadline := time.Now().Add(45 * time.Second)
	for {
		shutdown, err = startRuntime(c, logger)
		if err == nil {
			break
		}
		logger.Printf("startup attempt failed: %v", err)
		if time.Now().After(deadline) {
			logger.Printf("startup failed after retries: %v", err)
			return true, 1
		}
		checkpoint++
		status <- svc.Status{State: svc.StartPending, WaitHint: 15_000, CheckPoint: checkpoint}
		time.Sleep(2 * time.Second)
	}
	defer func() {
		logger.Printf("service stopping: shutting down runtime...")
		shutdown()
		logger.Printf("service stopping: cleaning up WinDivert driver...")
		if err := cleanupWinDivertDriver(); err != nil {
			logger.Printf("service stopped; WinDivert cleanup incomplete: %v", err)
		} else {
			logger.Printf("service stopped successfully")
		}
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	logger.Printf("service started and running")
	for change := range req {
		switch change.Cmd {
		case svc.Stop, svc.Shutdown:
			logger.Printf("received stop/shutdown command from SCM (cmd=%d)", change.Cmd)
			status <- svc.Status{State: svc.StopPending, WaitHint: 10_000, CheckPoint: 1}
			return false, 0
		case svc.Interrogate:
			status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
		}
	}
	logger.Printf("service control channel closed")
	return false, 0
}

func InstallService(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	config := fs.String("config", DefaultConfigPath(), "path to JSON config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	localConfig, err := PortableConfigPath(PortableRoot(), *config)
	if err != nil {
		return err
	}
	if !ConsoleBuild {
		if err := ValidatePortableServiceFolder(PortableRoot()); err != nil {
			return fmt.Errorf("service folder is not protected; use Install in the GUI to create a secure portable copy: %w", err)
		}
	}
	*config = localConfig
	if err = ensurePortableDirectory(PortableRoot(), filepath.Dir(localConfig)); err != nil {
		return err
	}
	if _, err := os.Stat(*config); errors.Is(err, os.ErrNotExist) {
		if err := appconfig.Save(*config, appconfig.Default()); err != nil {
			return err
		}
	}
	path, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	settings, err := appconfig.Load(path)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	existing, err := m.OpenService(Name)
	if err == nil {
		existing.Close()
		return errors.New("service already exists; run uninstall first")
	}
	if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	c := mgr.Config{
		DisplayName:      "SonDNS",
		Description:      "SonDNS encrypted DNS proxy with optional WinDivert interception",
		StartType:        serviceStartType(settings.ServiceAutoStart),
		ErrorControl:     windows.SERVICE_ERROR_NORMAL,
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		DelayedAutoStart: false,
		Dependencies:     []string{"Tcpip", "Dnscache"},
	}
	s, err := m.CreateService(Name, exe, c, "run", "--config="+path)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := syncGUIAutostart(path, settings.ServiceAutoStart); err != nil {
		return fmt.Errorf("service startup registration failed: %w", errors.Join(err, s.Delete()))
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("service installed but failed to start: %w", err)
	}
	return nil
}

func UninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	serviceExe := ""
	s, err := m.OpenService(Name)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return err
		}
		trayErr := syncGUIAutostart("", false)
		driverErr := cleanupWinDivertDriver()
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return exeErr
		}
		return errors.Join(trayErr, driverErr, purgeSonDNSInstall(exe))
	}
	defer s.Close()
	if err := requireThisService(s); err != nil {
		return err
	}
	if cfg, configErr := s.Config(); configErr == nil {
		serviceExe = serviceBinaryPath(cfg.BinaryPathName)
	}
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	if err := waitForServiceStopped(s, 10*time.Second); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return err
	}
	trayErr := syncGUIAutostart("", false)
	driverErr := cleanupWinDivertDriver()
	if serviceExe == "" {
		serviceExe, _ = os.Executable()
	}
	return errors.Join(trayErr, driverErr, purgeSonDNSInstall(serviceExe))
}

func serviceBinaryPath(commandLine string) string {
	commandLine = strings.TrimSpace(commandLine)
	if commandLine == "" {
		return ""
	}
	if commandLine[0] == '"' {
		if end := strings.IndexByte(commandLine[1:], '"'); end >= 0 {
			return commandLine[1 : end+1]
		}
	}
	if fields := strings.Fields(commandLine); len(fields) > 0 {
		return strings.Trim(fields[0], `"`)
	}
	return ""
}

// Removing the service keeps the portable folder intact. Configuration, logs,
// runtime and backups are user data; deleting them is a separate user action.
func purgeSonDNSInstall(installedExe string) error {
	return nil
}

func ServiceStatus() error {
	st, err := getServiceStatus()
	if err != nil {
		return err
	}
	fmt.Println(st.State)
	return nil
}

func getServiceStatus() (svc.Status, error) {
	s, err := openSonDNSService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return svc.Status{}, err
	}
	defer s.Close()
	return s.Query()
}

func RestartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := requireThisService(s); err != nil {
		return err
	}
	if !ConsoleBuild {
		if err := ValidatePortableServiceFolder(PortableRoot()); err != nil {
			return err // Leave the running service untouched on validation failure.
		}
	}
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
		if err := waitForServiceStopped(s, 10*time.Second); err != nil {
			return err
		}
	}
	return s.Start()
}

func waitForServiceStopped(s *mgr.Service, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err != nil {
			return err
		}
		if st.State == svc.Stopped {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timed out waiting for service to stop")
}

func StopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		_ = cleanupWinDivertDriver()
		return err
	}
	defer s.Close()
	if err := requireThisService(s); err != nil {
		return err
	}
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return cleanupWinDivertDriver()
	}
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	if err := waitForServiceStopped(s, 10*time.Second); err != nil {
		return err
	}
	return cleanupWinDivertDriver()
}

type runtimeEngine interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

// RunWindowsService dispatches the SCM protocol to the service runtime.
func RunWindowsService(path string) error { return svc.Run(Name, &windowsService{configPath: path}) }

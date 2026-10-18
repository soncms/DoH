package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "SonDNS"

func main() {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "SonDNS currently targets Windows because transparent interception uses WinDivert")
		os.Exit(2)
	}
	isService, _ := svc.IsWindowsService()
	if isService {
		configPath, err := serviceRunConfigPath(portableRoot(), os.Args[1:])
		if err != nil {
			os.Exit(1)
		}
		if err := svc.Run(serviceName, &windowsService{configPath: configPath}); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := cli(os.Args[1:]); err != nil {
		reportStartupError(err)
		os.Exit(1)
	}
}

func cli(args []string) error {
	if consoleBuild {
		return consoleCLI(args)
	}
	if len(args) == 0 {
		return runGUI()
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("SonDNS", buildVersion, "(portable GUI, embedded runtime)")
		return nil
	case "verify":
		root, err := ensureEmbeddedZapret2(nil)
		if err != nil {
			return err
		}
		fmt.Println("Embedded runtime verified:", root)
		return nil
	case "gui":
		fs := flag.NewFlagSet("gui", flag.ContinueOnError)
		config := fs.String("config", "", "path to JSON config (defaults to the owned service configuration)")
		tray := fs.Bool("tray", false, "start minimized in system tray")
		waitFor := fs.Uint("wait-for", 0, "wait for the previous SonDNS window to exit")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := waitForGUIHandoff(uint32(*waitFor)); err != nil {
			return err
		}
		return runGUIWithConfigAndTray(*config, *tray)
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		config := fs.String("config", defaultConfigPath(), "path to JSON config")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return runForeground(*config)
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		config := fs.String("config", defaultConfigPath(), "path to JSON config")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path, err := portableConfigPath(portableRoot(), *config)
		if err != nil {
			return err
		}
		return savePortableConfig(path, DefaultConfig())
	case "install":
		return installService(args[1:])
	case "uninstall":
		return uninstallService()
	case "stop":
		return stopService()
	case "status":
		return serviceStatus()
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Println(`SonDNS - portable encrypted DNS resolver for Windows

Commands:
	verify     extract/verify embedded runtime locally without starting drivers
	version    show build version
  init       create a default JSON config
  run        run in the foreground
  install    install and start (service_auto_start controls startup)
  stop       stop the Windows service without removing it
  uninstall  stop and remove the service
  status     show service status

Examples:
  SonDNS init
  SonDNS install
  SonDNS run --config data\sondns.json`)
}

func defaultConfigPath() string {
	return filepath.Join(portableRoot(), "data", "sondns.json")
}

func runForeground(path string) error {
	path, err := portableConfigPath(portableRoot(), path)
	if err != nil {
		return err
	}
	c, err := LoadConfig(path)
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

func startRuntime(c Config, logger *log.Logger) (func(), error) {
	instance, acquired, err := acquireProcessMutex("Global\\SonDNS.Runtime")
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

func newLogger(c Config) (*log.Logger, func(), error) {
	return newLoggerAt(portableRoot(), c)
}

func newLoggerAt(root string, c Config) (*log.Logger, func(), error) {
	if c.LogFile == "" {
		return log.New(os.Stderr, "sondns: ", log.LstdFlags), func() {}, nil
	}
	c.LogFile = filepath.Join(root, portableLogPath(c.LogFile))
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
	if !consoleBuild {
		if err := validatePortableServiceFolder(portableRoot()); err != nil {
			return true, 2
		}
	}
	c, err := LoadConfig(d.configPath)
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

func installService(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "path to JSON config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	localConfig, err := portableConfigPath(portableRoot(), *config)
	if err != nil {
		return err
	}
	if !consoleBuild {
		if err := validatePortableServiceFolder(portableRoot()); err != nil {
			return fmt.Errorf("service folder is not protected; use Install in the GUI to create a secure portable copy: %w", err)
		}
	}
	*config = localConfig
	if err = ensurePortableDirectory(portableRoot(), filepath.Dir(localConfig)); err != nil {
		return err
	}
	if _, err := os.Stat(*config); errors.Is(err, os.ErrNotExist) {
		if err := SaveConfig(*config, DefaultConfig()); err != nil {
			return err
		}
	}
	path, err := filepath.Abs(*config)
	if err != nil {
		return err
	}
	settings, err := LoadConfig(path)
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
	existing, err := m.OpenService(serviceName)
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
	s, err := m.CreateService(serviceName, exe, c, "run", "--config="+path)
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

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	serviceExe := ""
	s, err := m.OpenService(serviceName)
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

func serviceStatus() error {
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

func restartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := requireThisService(s); err != nil {
		return err
	}
	if !consoleBuild {
		if err := validatePortableServiceFolder(portableRoot()); err != nil {
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

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
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

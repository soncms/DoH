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
		configPath := defaultConfigPath()
		for _, arg := range os.Args[1:] {
			if strings.HasPrefix(arg, "--config=") {
				configPath = strings.TrimPrefix(arg, "--config=")
			}
		}
		if err := svc.Run(serviceName, &windowsService{configPath: configPath}); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := cli(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cli(args []string) error {
	if len(args) == 0 {
		return runGUI()
	}
	switch args[0] {
	case "gui":
		fs := flag.NewFlagSet("gui", flag.ContinueOnError)
		config := fs.String("config", defaultConfigPath(), "path to JSON config")
		tray := fs.Bool("tray", false, "start minimized in system tray")
		if err := fs.Parse(args[1:]); err != nil {
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
		return SaveConfig(*config, DefaultConfig())
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
	fmt.Println(`SonDNS - headless DNS-over-HTTPS resolver for Windows

Commands:
  init       create a default JSON config
  run        run in the foreground
  install    install and start (service_auto_start controls startup)
  stop       stop the Windows service without removing it
  uninstall  stop and remove the service
  status     show service status

Examples:
  SonDNS init --config C:\ProgramData\SonDNS\sondns.json
  SonDNS install --config C:\ProgramData\SonDNS\sondns.json
  SonDNS run --config .\sondns.json`)
}

func defaultConfigPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = "."
	}
	return filepath.Join(base, "SonDNS", "sondns.json")
}

func runForeground(path string) error {
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
		_ = cleanupWinDivertDriver()
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
	engine, resolver, err := NewDNSProxyEngine(c, logger)
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
	return func() {
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
	if c.LogFile == "" {
		return log.New(os.Stderr, "sondns: ", log.LstdFlags), func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(c.LogFile), 0o755); err != nil {
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
		_ = cleanupWinDivertDriver()
		logger.Printf("service stopped successfully")
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
	_ = syncGUIAutostart(settings.ServiceAutoStart)
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
	s, err := m.OpenService(serviceName)
	if err != nil {
		_ = cleanupWinDivertDriver()
		return err
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return err
	}
	if err := waitForServiceStopped(s, 10*time.Second); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return err
	}
	_ = syncGUIAutostart(false)
	return cleanupWinDivertDriver()
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

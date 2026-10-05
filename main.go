package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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

var buildVersion = "dev"

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
		usage()
		return errors.New("a service command is required; launch SonDNS.exe for the graphical interface")
	}
	switch args[0] {
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
		return runPrivileged(func() error { return stopService() })
	case "start":
		return runPrivileged(func() error { return startInstalledService() })
	case "restart":
		return runPrivileged(func() error { return restartService() })
	case "autostart":
		fs := flag.NewFlagSet("autostart", flag.ContinueOnError)
		config := fs.String("config", defaultConfigPath(), "path to JSON config")
		enable := fs.Bool("enable", false, "enable start with Windows")
		disable := fs.Bool("disable", false, "disable start with Windows")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *enable == *disable {
			return errors.New("autostart requires exactly one of --enable or --disable")
		}
		return runPrivileged(func() error { return setAutostart(*config, *enable) })
	case "status":
		jsonOutput := false
		for _, arg := range args[1:] {
			if arg == "--json" {
				jsonOutput = true
			}
		}
		if jsonOutput {
			return serviceStatusJSON()
		}
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
  start      start an installed Windows service
  restart    restart the Windows service
  autostart  atomically update service and UI start with Windows (--enable/--disable)
  uninstall  stop and remove the service
  status     show service status (use --json for machine-readable output)

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
	shutdown, err := startRuntime(c, logger, path)
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

func startRuntime(c Config, logger *log.Logger, configPath string) (func(), error) {
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
	engine.setConfigPath(configPath)
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
	engine.setDPIBackend(dpi)
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
		engine.setDPIBackend(nil)
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
		shutdown, err = startRuntime(c, logger, d.configPath)
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
	return runPrivileged(func() error { return installServiceElevated(args) })
}

func installServiceElevated(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "path to JSON config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := filepath.Abs(*config)
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
		settings, settingsErr := LoadConfig(path)
		if settingsErr != nil {
			settings = DefaultConfig()
		}
		return migrateExistingService(existing, path, settings, exe)
	}
	if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := SaveConfig(path, DefaultConfig()); err != nil {
			return err
		}
	}
	settings, err := LoadConfig(path)
	if err != nil {
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
	oldRegistry, oldRegistryPresent, err := readGUIAutostart()
	if err != nil {
		_ = s.Delete()
		return err
	}
	if err := syncGUIAutostart(settings.ServiceAutoStart); err != nil {
		_ = s.Delete()
		return fmt.Errorf("update SonDNS tray autostart: %w", err)
	}
	if err := s.Start(); err != nil {
		_ = writeGUIAutostart(oldRegistry, oldRegistryPresent)
		_ = s.Delete()
		return fmt.Errorf("service installed but failed to start: %w", err)
	}
	return nil
}

// migrateExistingService upgrades the service in place so installing a new
// build does not discard the user's config, log, or SCM settings. The service
// object remains named SonDNS, which keeps existing SCM registrations and
// scheduled starts compatible with older releases.
func migrateExistingService(s *mgr.Service, configPath string, settings Config, newExe string) error {
	defer s.Close()
	oldConfig, err := s.Config()
	if err != nil {
		return err
	}
	oldCommand := oldConfig.BinaryPathName
	oldConfigPath := serviceConfigPath(oldCommand)
	if oldConfigPath == "" {
		oldConfigPath = configPath
	}
	if legacySettings, loadErr := LoadConfig(oldConfigPath); loadErr == nil {
		settings = legacySettings
	}
	if _, err := os.Stat(oldConfigPath); err != nil {
		return fmt.Errorf("legacy service config %s is unavailable: %w", oldConfigPath, err)
	}
	backupPath := fmt.Sprintf("%s.backup-%s", oldConfigPath, time.Now().Format("20060102-150405"))
	if err := copyFile(oldConfigPath, backupPath); err != nil {
		return fmt.Errorf("backup legacy config: %w", err)
	}

	status, err := s.Query()
	if err != nil {
		return err
	}
	oldRegistry, oldRegistryPresent, err := readGUIAutostart()
	if err != nil {
		return err
	}
	wasRunning := status.State != svc.Stopped
	if wasRunning {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
		if err := waitForServiceStopped(s, 15*time.Second); err != nil {
			return err
		}
	}

	newConfig := oldConfig
	newConfig.BinaryPathName = quoteWindowsArgument(newExe) + " run --config=" + quoteWindowsArgument(oldConfigPath)
	newConfig.StartType = serviceStartType(settings.ServiceAutoStart)
	rollback := func() {
		_ = s.UpdateConfig(oldConfig)
		_ = writeGUIAutostart(oldRegistry, oldRegistryPresent)
		if wasRunning {
			_ = s.Start()
		}
	}
	if err := s.UpdateConfig(newConfig); err != nil {
		rollback()
		return fmt.Errorf("update service binary path: %w", err)
	}
	if err := syncGUIAutostart(settings.ServiceAutoStart); err != nil {
		rollback()
		return fmt.Errorf("update SonDNS tray autostart: %w", err)
	}
	if err := s.Start(); err != nil {
		rollback()
		return fmt.Errorf("start migrated service: %w", err)
	}
	return nil
}

func serviceConfigPath(commandLine string) string {
	args, err := windows.DecomposeCommandLine(commandLine)
	if err != nil {
		return ""
	}
	for i, arg := range args {
		if strings.HasPrefix(strings.ToLower(arg), "--config=") {
			return strings.Trim(strings.TrimSpace(arg[len("--config="):]), `"`)
		}
		if strings.EqualFold(arg, "--config") && i+1 < len(args) {
			return strings.Trim(strings.TrimSpace(args[i+1]), `"`)
		}
	}
	return ""
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func uninstallService() error {
	return runPrivileged(uninstallServiceElevated)
}

func uninstallServiceElevated() error {
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
		_ = syncGUIAutostart(false)
		if err := cleanupWinDivertDriver(); err != nil {
			return err
		}
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return exeErr
		}
		return purgeSonDNSInstall(exe)
	}
	defer s.Close()
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
	_ = syncGUIAutostart(false)
	if err := cleanupWinDivertDriver(); err != nil {
		return err
	}
	if serviceExe == "" {
		serviceExe, _ = os.Executable()
	}
	return purgeSonDNSInstall(serviceExe)
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

// purgeSonDNSInstall removes only data owned by this installation. The
// executable, GUI and workspace source/dist files are intentionally kept so
// the user can install the service again immediately.
func purgeSonDNSInstall(installedExe string) error {
	base := strings.TrimSpace(os.Getenv("ProgramData"))
	if base == "" {
		return errors.New("ProgramData is unavailable; refusing to purge install files")
	}
	appDir := filepath.Join(base, "SonDNS")
	if err := os.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove %s: %w", appDir, err)
	}
	if installedExe == "" {
		return nil
	}
	installedExe, err := filepath.Abs(installedExe)
	if err != nil {
		return err
	}
	baseName := filepath.Base(installedExe)
	if !strings.EqualFold(baseName, "SonDNS.Service.exe") && !strings.EqualFold(baseName, "SonDNS.exe") {
		return nil
	}
	dir := filepath.Dir(installedExe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "SonDNS.Service.exe.bak-") || strings.HasPrefix(name, "SonDNS.Service.exe.backup-") || name == "SonDNS.Service.exe~" ||
			strings.HasPrefix(name, "SonDNS.exe.bak-") || strings.HasPrefix(name, "SonDNS.exe.backup-") || name == "SonDNS.exe~" {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
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

func serviceStatusJSON() error {
	snapshot, err := queryServiceSnapshot()
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Exists         bool   `json:"exists"`
		State          string `json:"state"`
		StartType      uint32 `json:"start_type"`
		AutoStart      bool   `json:"auto_start"`
		BinaryPathName string `json:"binary_path"`
	}{
		Exists:         snapshot.Exists,
		State:          serviceStateName(snapshot.State),
		StartType:      snapshot.StartType,
		AutoStart:      snapshot.autoStart(),
		BinaryPathName: snapshot.BinaryPathName,
	})
}

func setAutostart(configPath string, enable bool) error {
	oldConfig, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	oldRegistry, oldRegistryPresent, err := readGUIAutostart()
	if err != nil {
		return err
	}
	snapshot, serviceErr := queryServiceSnapshot()
	servicePresent := serviceErr == nil && snapshot.Exists
	if serviceErr != nil && !errors.Is(serviceErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return serviceErr
	}
	if servicePresent {
		if err := setServiceAutoStart(enable); err != nil {
			return err
		}
	}
	if err := syncGUIAutostart(enable); err != nil {
		if servicePresent {
			_ = setServiceAutoStart(snapshot.autoStart())
		}
		return err
	}
	updated := oldConfig
	updated.ServiceAutoStart = enable
	if err := SaveConfig(configPath, updated); err != nil {
		_ = writeGUIAutostart(oldRegistry, oldRegistryPresent)
		if servicePresent {
			_ = setServiceAutoStart(snapshot.autoStart())
		}
		return err
	}
	return nil
}

func serviceStateName(state svc.State) string {
	switch state {
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
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

//go:build windows && console

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"sondns/internal/buildinfo"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
)

func ConsoleCLI(args []string) error {
	root := PortableRoot()
	if len(args) == 0 {
		return consoleMenu(root, os.Stdin, os.Stdout)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if args[0] == "elevated" {
		err := ConsoleCLI(args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		fmt.Print("Press Enter to close...")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return err
	}
	if needsAdmin(args) && !windows.GetCurrentProcessToken().IsElevated() {
		return openElevated(args)
	}
	switch args[0] {
	case "menu":
		return consoleMenu(root, os.Stdin, os.Stdout)
	case "help", "--help", "-h":
		consoleUsage(os.Stdout)
		return nil
	case "version", "--version":
		fmt.Println("SonDNS", buildinfo.Version, "(CMD, official upstream runtime)")
		return nil
	case "status", "doctor":
		return consoleDoctor(ctx, root, os.Stdout, args[0] == "doctor")
	case "config":
		if len(args) == 1 || args[1] == "show" {
			c, err := consoleConfig(root)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(c, "", "  ")
			fmt.Println(string(b))
			return nil
		}
		if len(args) == 4 && args[1] == "listen" {
			return errors.New("use config listen host:port")
		}
		if len(args) == 3 && args[1] == "listen" {
			return editConsoleConfig(root, func(c *appconfig.Config) error {
				if _, _, err := net.SplitHostPort(args[2]); err != nil {
					return err
				}
				c.Listen = args[2]
				return nil
			}, true, os.Stdout)
		}
	case "dns":
		return consoleDNS(root, args[1:])
	case "dpi":
		if len(args) == 2 {
			return editConsoleConfig(root, func(c *appconfig.Config) error {
				switch args[1] {
				case "off":
					c.DPI.Enabled = false
				case "default", "tcp", "tcp+udp":
					c.DPI.Enabled = true
					c.DPI.CaptureMode = args[1]
				default:
					return errors.New("DPI mode: off, default, tcp, tcp+udp")
				}
				return nil
			}, true, os.Stdout)
		}
	case "service":
		return consoleServiceCommand(root, args[1:])
	case "components":
		if len(args) != 2 {
			break
		}
		switch args[1] {
		case "list", "verify":
			return showComponents(root, os.Stdout)
		case "check":
			_, err := checkComponents(ctx, root, os.Stdout)
			return err
		case "update":
			return updateComponents(ctx, root, os.Stdout)
		case "rollback":
			return rollbackComponents(ctx, root, os.Stdout)
		}
	case "logs":
		return consoleLogs(ctx, root, args[1:], os.Stdout)
	case "preview":
		fs := flag.NewFlagSet("preview", flag.ContinueOnError)
		listen := fs.String("listen", "127.0.0.1:15300", "isolated loopback listener")
		duration := fs.Duration("duration", 0, "automatically stop after this interval")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *duration > 0 {
			var stop context.CancelFunc
			ctx, stop = context.WithTimeout(ctx, *duration)
			defer stop()
		}
		return runConsolePreview(ctx, root, *listen, nil, os.Stdout)
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		path := fs.String("config", DefaultConfigPath(), "JSON configuration")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return RunForeground(*path)
	}
	consoleUsage(os.Stdout)
	return fmt.Errorf("invalid command: %s", strings.Join(args, " "))
}

func consoleUsage(out io.Writer) {
	fmt.Fprintln(out, `SonDNS CMD
  menu                              interactive control panel (default)
  status | doctor | version          state / checks / build version
  dns use SonCMS|NextDNS             change provider
  dns custom https://...             set an encrypted upstream
  dns dnssec on|off                  request DNSSEC records
  dpi off|default|tcp|tcp+udp         change DPI mode
  config show                       show this folder's settings
  config listen 127.0.0.1:5300       change the DNS listener
  logs [--follow] [--preview]        latest 200 DNS rows / live view
  logs --clear [--preview]           clear RAM log
  service install|bind|start|stop|restart|remove
  service auto on|off                service startup with Windows
  components list|check|update|rollback|verify
  preview [--listen 127.0.0.1:15300] [--duration 30s]
  run [--config PATH]                foreground runtime (Administrator)

Preview uses loopback only, with no WinDivert/DPI and no SCM changes.
Service/remove keeps configuration, runtime files and backups.`)
}

func needsAdmin(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "service", "dns", "dpi", "run":
		return true
	case "config":
		return len(args) > 1 && args[1] != "show"
	case "logs":
		for _, arg := range args[1:] {
			if arg == "--clear" {
				return true
			}
		}
	case "components":
		return len(args) > 1 && (args[1] == "update" || args[1] == "rollback")
	}
	return false
}

func openElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{"elevated"}, args...)))
	dir, _ := windows.UTF16PtrFromString(PortableRoot())
	if err = windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL); err != nil {
		return err
	}
	fmt.Println("Opened an Administrator console for this action.")
	return nil
}

func consoleDNS(root string, args []string) error {
	if len(args) != 2 {
		return errors.New("use dns use SonCMS|NextDNS, dns custom URL, or dns dnssec on|off")
	}
	return editConsoleConfig(root, func(c *appconfig.Config) error {
		switch args[0] {
		case "use":
			name := args[1]
			if strings.EqualFold(name, appconfig.ProviderSonCMS) {
				name = appconfig.ProviderSonCMS
			} else if strings.EqualFold(name, appconfig.ProviderNextDNS) {
				name = appconfig.ProviderNextDNS
			} else {
				return errors.New("unknown provider")
			}
			enabled := appconfig.DNSSECEnabled(*c)
			u := appconfig.PredefinedUpstream(name)
			u.DNSSEC = enabled
			c.Upstreams = []appconfig.Upstream{u}
		case "custom":
			enabled := appconfig.DNSSECEnabled(*c)
			c.Upstreams = []appconfig.Upstream{{URL: args[1], DNSSEC: enabled}}
		case "dnssec":
			enabled, err := parseToggle(args[1])
			if err != nil {
				return err
			}
			for i := range c.Upstreams {
				c.Upstreams[i].DNSSEC = enabled
			}
		default:
			return errors.New("unknown DNS operation")
		}
		return nil
	}, false, os.Stdout)
}

func parseToggle(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, errors.New("choose on or off")
}

func consoleServiceCommand(root string, args []string) error {
	if len(args) == 0 {
		return errors.New("choose install/bind/start/stop/restart/remove/auto")
	}
	return withConsoleMutation(root, func() error {
		if args[0] == "install" || args[0] == "bind" {
			return bindConsoleService(root)
		}
		if args[0] == "auto" && len(args) == 2 {
			enabled, err := parseToggle(args[1])
			if err != nil {
				return err
			}
			c, err := consoleConfig(root)
			if err != nil {
				return err
			}
			_, _, own, err := consoleService(root)
			if err != nil {
				return err
			}
			if own {
				if err = setServiceAutoStart(enabled); err != nil {
					return err
				}
			}
			old := c.ServiceAutoStart
			c.ServiceAutoStart = enabled
			if err = appconfig.Save(filepath.Join(root, "data", "sondns.json"), c); err != nil && own {
				return errors.Join(err, setServiceAutoStart(old))
			}
			return err
		}
		if err := requireOwnService(root); err != nil {
			return err
		}
		switch args[0] {
		case "start":
			if err := StartInstalledService(); err != nil {
				return err
			}
			return waitRunning(50 * time.Second)
		case "stop":
			return StopService()
		case "restart":
			if err := RestartService(); err != nil {
				return err
			}
			return waitRunning(50 * time.Second)
		case "remove":
			return unbindConsoleService(root)
		}
		return errors.New("unknown service action")
	})
}

func consoleDoctor(ctx context.Context, root string, out io.Writer, probe bool) error {
	fmt.Fprintf(out, "SonDNS %s | Administrator: %v\nFolder: %s\n", buildinfo.Version, windows.GetCurrentProcessToken().IsElevated(), root)
	c, err := consoleConfig(root)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved: provider=%s | DNSSEC=%v | DPI=%v %s | listen=%s | startup=%v\n", c.CurrentProvider(), appconfig.DNSSECEnabled(c), c.DPI.Enabled, c.DPI.CaptureMode, c.Listen, c.ServiceAutoStart)
	service, st, own, err := consoleService(root)
	if err != nil {
		return err
	}
	if service.BinaryPathName == "" {
		fmt.Fprintln(out, "Service: not installed")
	} else {
		states := map[svc.State]string{svc.Stopped: "Stopped", svc.StartPending: "Starting", svc.StopPending: "Stopping", svc.Running: "Running", svc.Paused: "Paused"}
		fmt.Fprintf(out, "Service: %s, this folder=%v\n  %s\n", states[st.State], own, service.BinaryPathName)
	}
	if !own && service.BinaryPathName != "" {
		fmt.Fprintln(out, "Another installation owns SonDNS. Preview is available; Service > Bind switches installations.")
	}
	state, pipeErr := control.CallAt(consolePipe(root, false), control.Request{Op: "state"})
	if pipeErr == nil {
		b, _ := json.Marshal(state.State)
		fmt.Fprintln(out, "Live:", string(b))
	} else {
		fmt.Fprintln(out, "This folder's runtime: offline")
	}
	if err = showComponents(root, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Runtime hashes: OK")
	if probe {
		lock, loadErr := loadComponents(root)
		if loadErr != nil {
			return loadErr
		}
		if err = validateWinwsCandidate(ctx, root, lock, c.DPI); err != nil {
			return err
		}
		fmt.Fprintln(out, "Original winws2/WinDivert preflight: OK")
	}
	if probe && own && st.State == svc.Running {
		if err = probeConsoleDNS(ctx, c.Listen); err != nil {
			return err
		}
		fmt.Fprintln(out, "UDP/TCP DNS: OK")
	}
	return nil
}

func consoleLogs(ctx context.Context, root string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "follow live rows; Ctrl+C returns")
	preview := fs.Bool("preview", false, "preview runtime")
	clearLog := fs.Bool("clear", false, "clear RAM rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pipe := consolePipe(root, *preview)
	if *clearLog {
		_, err := control.CallAt(pipe, control.Request{Op: "clear_log"})
		return err
	}
	var last uint64
	var instance string
	fmt.Fprintln(out, "TIME         DOMAIN                                       TYPE  RESULT      MS      ANSWERS")
	for {
		r, err := control.CallAt(pipe, control.Request{Op: "snapshot"})
		if err != nil {
			return err
		}
		if r.InstanceID != instance {
			last = 0
			instance = r.InstanceID
		}
		for _, row := range r.Entries {
			if row.ID <= last {
				continue
			}
			fmt.Fprintf(out, "%-12s %-44.44s %-5s %-10s %-7.1f %s\n", row.CompletedTime.Local().Format("15:04:05.000"), safeConsole(row.Domain), safeConsole(row.Type), safeConsole(row.Result), float64(row.DurationUS)/1000, safeConsole(strings.Join(row.AnswerIPs, ",")))
			if row.Error != "" {
				fmt.Fprintln(out, "  "+safeConsole(row.Error))
			}
			last = row.ID
		}
		if !*follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func safeConsole(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

func probeConsoleDNS(ctx context.Context, address string) error {
	for _, network := range []string{"udp", "tcp"} {
		q := new(dns.Msg)
		q.SetQuestion("example.com.", dns.TypeA)
		r, _, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).ExchangeContext(ctx, q, address)
		if err != nil {
			return err
		}
		if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
			return fmt.Errorf("%s DNS check failed: %s", network, dns.RcodeToString[r.Rcode])
		}
	}
	return nil
}

func validateConsoleCandidate(ctx context.Context, root string, lock componentLock, c appconfig.Config) error {
	if err := validateWinwsCandidate(ctx, root, lock, c.DPI); err != nil {
		return err
	}
	c.Listen = "127.0.0.1:0"
	e := newConsoleEngine(c, root, lock, log.New(io.Discard, "", 0))
	e.pipe = consolePipe(root, true) + "-check-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := e.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(stop)
	}()
	return probeConsoleDNS(ctx, e.cfg.Listen)
}

func validateWinwsCandidate(ctx context.Context, root string, lock componentLock, cfg appconfig.DPIConfig) error {
	dir, err := componentPath(root, lock.Components["zapret2"])
	if err != nil {
		return err
	}
	args := []string{"--version"}
	if cfg.Enabled && cfg.Backend == "embedded-zapret2" {
		args, err = zapret2Arguments(dir, cfg)
		if err != nil {
			return err
		}
		// Upstream normally rejects a duplicate filter even for --dry-run.
		// Skip that mutex only for validation while the installed service runs.
		args = append([]string{"--dry-run", "--wf-dup-check=0"}, args...)
	}
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(check, filepath.Join(dir, "winws2.exe"), args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("unmodified winws2/WinDivert preflight: %w: %s", err, b)
	}
	return nil
}

func runConsolePreview(ctx context.Context, root, address string, reader *bufio.Reader, out io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("preview requires an explicit loopback IP")
	}
	lease, ok, err := AcquireProcessMutex(`Local\SonDNS.Preview.` + installationID(root))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("a preview is already running")
	}
	defer lease.Close()
	c, err := consoleConfig(root)
	if err != nil {
		return err
	}
	c.Listen = address
	c.DPI.Enabled = false
	lock, err := loadComponents(root)
	if err != nil {
		return err
	}
	e := newConsoleEngine(c, root, lock, log.New(out, "", log.LstdFlags))
	e.pipe = consolePipe(root, true)
	if err = e.Start(ctx); err != nil {
		return err
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(stop)
	}()
	fmt.Fprintln(out, "Preview running:", e.cfg.Listen, "| no WinDivert/DPI | installed service unchanged")
	if err = probeConsoleDNS(ctx, e.cfg.Listen); err != nil {
		return err
	}
	fmt.Fprintln(out, "Official dnsproxy: UDP/TCP DNS checks passed.")
	if reader != nil {
		fmt.Fprint(out, "Press Enter to stop preview and return to menu...")
		_, err = reader.ReadString('\n')
		return err
	}
	<-ctx.Done()
	return nil
}

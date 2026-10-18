//go:build windows && !console

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Explicit opt-in only. Tests the real SCM with WinDivert enabled and restores the
// absent service state on exit. Never replaces a user's installed SonDNS service.
func TestWindowsServiceLifecycle(t *testing.T) {
	if os.Getenv("SONDNS_SERVICE_TEST") != "1" {
		t.Skip("set SONDNS_SERVICE_TEST=1 as Administrator")
	}
	before, err := queryServiceSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.Exists {
		t.Skip("SonDNS already installed; will not alter it")
	}
	tray, err := readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey)
	if err != nil {
		t.Fatal(err)
	}
	if tray.Exists {
		t.Skip("SonDNS tray startup already exists; will not alter it")
	}
	scm, err := mgr.Connect()
	if err != nil {
		t.Fatal(err)
	}
	driver, driverErr := scm.OpenService("WinDivert")
	if driverErr == nil {
		_ = driver.Close()
	}
	_ = scm.Disconnect()
	if driverErr == nil {
		t.Skip("WinDivert already installed; leave the driver untouched")
	}
	if !errors.Is(driverErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		t.Fatal(driverErr)
	}
	built, err := filepath.Abs(filepath.Join("dist", "SonDNS-Portable", "SonDNS.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if override := os.Getenv("SONDNS_TEST_EXE"); override != "" {
		built, err = filepath.Abs(override)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(built); err != nil {
		t.Fatal("build SonDNS.exe before this test")
	}
	config := DefaultConfig()
	held, release := make(chan struct{}), make(chan struct{})
	releaseHeld := sync.OnceFunc(func() { close(release) })
	var heldOnce sync.Once
	stamp, fixtureCalls := startLocalEncryptedDNSFixtureWithGate(t, func(ctx context.Context, q *dns.Msg) error {
		if len(q.Question) != 1 || q.Question[0].Name != "sondns-hold.invalid." {
			return nil
		}
		heldOnce.Do(func() { close(held) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	t.Cleanup(releaseHeld)
	ipv6 := prepareLifecycleIPv6(t)
	config.Upstreams = []Upstream{{URL: stamp}}
	config.Interception = "windivert"
	config.LogFile = filepath.Join("data", "system.log")
	packet, ln := listenTestDNSPair(t)
	config.Listen = ln.Addr().String()
	_ = packet.Close()
	_ = ln.Close()
	config.Timeout = 3 * time.Second
	parent := os.Getenv("SONDNS_SERVICE_TEST_PARENT")
	if parent == "" {
		parent = filepath.VolumeName(built) + `\`
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "SonDNS-service-test-"+rand.Text())
	binary, err := createSecurePortableCopy(built, dir, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if remaining, err := queryServiceSnapshot(); err != nil || remaining.Exists {
			t.Logf("Retaining service fixture at %s: state=%+v err=%v", dir, remaining, err)
			return
		}
		manager, err := mgr.Connect()
		if err != nil {
			t.Errorf("retain fixture after SCM query failure: %v", err)
			return
		}
		driver, driverErr := manager.OpenService(winDivertServiceName)
		if driverErr == nil {
			_ = driver.Close()
		}
		_ = manager.Disconnect()
		if !errors.Is(driverErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			t.Logf("Retaining fixture at %s while driver registration may remain: %v", dir, driverErr)
			return
		}
		if insideRoot(parent, dir) && strings.HasPrefix(filepath.Base(dir), "SonDNS-service-test-") {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("fixture cleanup: %v", err)
			}
		}
	})
	logPath := filepath.Join(dir, "data", "system.log")
	t.Cleanup(func() {
		if destination := os.Getenv("SONDNS_SERVICE_TEST_LOG"); destination != "" {
			contents, err := os.ReadFile(logPath)
			if err == nil {
				err = os.WriteFile(destination, contents, 0o600)
			}
			if err != nil {
				t.Errorf("save service runtime log: %v", err)
			}
		}
	})
	path := filepath.Join(dir, "data", "custom saved.json")
	checkGUIReadOnly := func(implicit bool) {
		t.Helper()
		before, err := queryServiceSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		configBefore, _ := os.ReadFile(path)
		args := []string{"gui", "--config", path}
		if implicit {
			args = []string{"gui"}
		}
		gui := exec.Command(binary, args...)
		if err = gui.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = gui.Process.Kill(); _ = gui.Wait() }()
		time.Sleep(500 * time.Millisecond)
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(gui.Process.Pid))
		if err != nil {
			t.Fatal(err)
		}
		var exitCode uint32
		err = windows.GetExitCodeProcess(h, &exitCode)
		_ = windows.CloseHandle(h)
		if err != nil || exitCode != 259 {
			t.Fatalf("GUI exited during read-only startup: exit=%d error=%v", exitCode, err)
		}
		after, err := queryServiceSnapshot()
		if err != nil || after != before {
			t.Fatalf("opening GUI mutated service: before=%+v after=%+v %v", before, after, err)
		}
		configAfter, _ := os.ReadFile(path)
		if string(configBefore) != string(configAfter) {
			t.Fatal("opening GUI rewrote settings")
		}
	}
	command := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, output)
		}
	}
	wait := func(state svc.State) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			s, e := queryServiceSnapshot()
			if e == nil && s.Exists && s.State == state {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("service did not reach %v", state)
	}
	cleanup := func() {
		if s, e := queryServiceSnapshot(); e == nil && s.Exists {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if e = exec.CommandContext(ctx, binary, "uninstall").Run(); e != nil {
				t.Errorf("cleanup failed: %v", e)
			}
		}
	}
	defer cleanup()
	for _, auto := range []bool{false, true} {
		config.ServiceAutoStart = auto
		config.DPI.Enabled = auto
		if err = SaveConfig(path, config); err != nil {
			t.Fatal(err)
		}
		checkGUIReadOnly(false)
		command("install", "--config", path)
		wait(svc.Running)
		snap, err := queryServiceSnapshot()
		if err != nil || snap.StartType != serviceStartType(auto) {
			t.Fatalf("wrong installed mode: %+v %v", snap, err)
		}
		t.Logf("installed: %s", snap.text())
		func() {
			s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := requireServiceConfigForExe(s, binary, path); err != nil {
				t.Fatal(err)
			}
			binding, err := s.Config()
			if err != nil {
				t.Fatal(err)
			}
			selected, err := implicitGUIConfigAt(binary, &binding)
			if err != nil || !strings.EqualFold(selected, path) {
				t.Fatalf("implicit GUI lost custom config: %s %v", selected, err)
			}
			tray, err := readMigrationTray(registry.LOCAL_MACHINE, runRegistryKey)
			if err != nil {
				t.Fatal(err)
			}
			if auto {
				want, err := guiTrayCommand(binary, path)
				if err != nil || !tray.Exists || tray.Value != want {
					t.Fatalf("tray lost custom service config: %+v %v", tray, err)
				}
			} else if tray.Exists {
				t.Fatal("manual service unexpectedly has tray startup")
			}
		}()
		checkGUIReadOnly(true)
		helperPath := filepath.Join(embeddedRuntimePath(dir), "winws2.exe")
		var dpiProcess *lifecycleDPIProcess
		if auto {
			dpiProcess = waitForLifecycleDPI(t, helperPath, 0)
			oldPID := dpiProcess.pid
			if err := windows.TerminateProcess(dpiProcess.handle, 1); err != nil {
				t.Fatal(err)
			}
			requireLifecycleDPIExited(t, dpiProcess)
			dpiProcess = waitForLifecycleDPI(t, helperPath, oldPID)
			t.Logf("embedded DPI helper restarted after forced exit: %d -> %d", oldPID, dpiProcess.pid)
		}
		if !auto {
			// A real local UDP and TCP query exercises the service and RAM log IPC.
			for _, network := range []string{"udp", "tcp"} {
				msg := new(dns.Msg)
				msg.SetQuestion("sondns-test.invalid.", dns.TypeA)
				reply, _, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).Exchange(msg, config.Listen)
				if err != nil {
					t.Fatal(err)
				}
				requireEncryptedFixtureReply(t, msg, reply)
			}
			// TEST-NET-3 has no resolver. A pinned synthetic answer here proves
			// transparent IPv4 UDP/53 interception rather than the local listener.
			msg := new(dns.Msg)
			msg.SetQuestion("sondns-test.invalid.", dns.TypeA)
			reply, _, err := (&dns.Client{Net: "udp", Timeout: 5 * time.Second}).Exchange(msg, "203.0.113.53:53")
			if err != nil {
				t.Fatal(err)
			}
			requireEncryptedFixtureReply(t, msg, reply)
			ipv6.report.Intercepted = ipv6.probe(t, true)
			t.Log("transparent IPv6 documentation and scoped link-local A/AAAA replies verified")
			if fixtureCalls.Load() < 3 {
				t.Fatal("service bypassed the encrypted fixture")
			}
			var rows []DNSRequestEntry
			synthetic := 0
			for n := 0; n < 20; n++ {
				response, readErr := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
				err = readErr
				rows = response.Entries
				synthetic = 0
				for _, row := range rows {
					if row.Domain == "sondns-test.invalid" {
						synthetic++
					}
				}
				if synthetic >= 3 {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil || synthetic < 3 {
				t.Fatalf("service didn't log all synthetic queries: %+v %v", rows, err)
			}
			var lastID uint64
			for _, row := range rows {
				lastID = max(lastID, row.ID)
			}
			t.Logf("DNS log: %d synthetic requests, last observed ID %d", synthetic, lastID)
			response, err := callDNSIPC(dnsIPCRequest{Op: "clear_log"})
			if err != nil || response.Version == 0 {
				t.Fatal(err)
			}
			response, err = callDNSIPC(dnsIPCRequest{Op: "snapshot"})
			if err != nil {
				t.Fatal(err)
			}
			// Background Windows queries may complete after Clear. They must
			// have fresh IDs; none of the previously observed rows may survive.
			for _, row := range response.Entries {
				if row.ID <= lastID {
					t.Fatal("clear retained a previously observed request")
				}
			}
			runLifecycleAccountAccess(t, path, config)
			// The fixture is a copied EXE. Test process ownership intentionally
			// differs, so use a handle verified against that exact fixture path.
			func() {
				scm, err := mgr.Connect()
				if err != nil {
					t.Fatal(err)
				}
				defer scm.Disconnect()
				s, err := scm.OpenService(serviceName)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				bound, err := s.Config()
				if err != nil || !strings.EqualFold(serviceBinaryPath(bound.BinaryPathName), binary) {
					t.Fatalf("fixture service binding changed: %+v %v", bound, err)
				}
				if err = updateServiceStartup(scmStartupController{s}, true); err != nil {
					t.Fatal(err)
				}
				// The upstream gate proves a synthetic packet is in flight in the
				// real interceptor. Keep its reply held until SCM reports Stopped.
				client := &dns.Client{Net: "udp", Timeout: 10 * time.Second}
				queryConn, err := client.Dial("203.0.113.53:53")
				if err != nil {
					t.Fatal(err)
				}
				defer queryConn.Close()
				defer releaseHeld()
				queryDone := make(chan error, 1)
				go func() {
					q := new(dns.Msg)
					q.SetQuestion("sondns-hold.invalid.", dns.TypeA)
					_, _, err := client.ExchangeWithConn(q, queryConn)
					queryDone <- err
				}()
				select {
				case <-held:
				case <-time.After(5 * time.Second):
					t.Fatal("intercepted query did not reach the upstream gate")
				}
				select {
				case err := <-queryDone:
					t.Fatalf("held query completed before service stop: %v", err)
				default:
				}
				stopping := time.Now()
				if _, err = s.Control(svc.Stop); err != nil {
					t.Fatal(err)
				}
				wait(svc.Stopped)
				if elapsed := time.Since(stopping); elapsed > 5*time.Second {
					t.Fatalf("in-flight stop exceeded bound: %s", elapsed)
				} else {
					t.Logf("in-flight intercepted query held upstream during stop: %s", elapsed)
				}
				// miekg/dns applies context deadlines, but cancellation alone
				// does not interrupt its UDP read. Close this test client's socket.
				_ = queryConn.Close()
				releaseHeld()
				select {
				case err := <-queryDone:
					if err == nil {
						t.Fatal("held upstream query unexpectedly received a DNS reply")
					}
				case <-time.After(time.Second):
					t.Fatal("synthetic client did not finish after cancellation")
				}
				if err = s.Start(); err != nil {
					t.Fatal(err)
				}
				wait(svc.Running)
				snap, err = queryServiceSnapshot()
				if err != nil || snap.StartType != windows.SERVICE_AUTO_START {
					t.Fatal("restart reset autostart")
				}
				if err = updateServiceStartup(scmStartupController{s}, false); err != nil {
					t.Fatal(err)
				}
			}()
		}
		if auto {
			// No harness DNS queries in this interval. Windows background DNS
			// is not suppressed, so this is not proof of globally idle traffic.
			time.Sleep(500 * time.Millisecond)
		}
		stopping := time.Now()
		command("uninstall")
		if auto {
			if elapsed := time.Since(stopping); elapsed > 5*time.Second {
				t.Fatalf("quiet stop exceeded bound: %s", elapsed)
			} else {
				t.Logf("quiet stop completed (OS background traffic not excluded): %s", elapsed)
			}
			requireLifecycleDPIExited(t, dpiProcess)
			requireNoLifecycleDPI(t, helperPath)
			t.Log("embedded DPI helper stopped without orphan")
		}
		snap, err = queryServiceSnapshot()
		if err != nil || snap.Exists {
			t.Fatalf("uninstall incomplete: %+v %v", snap, err)
		}
	}
	runLifecycleMigration(t, built, binary, path, parent, config)
	// Ensure saved config remains valid JSON after the full lifecycle.
	bytes, err := os.ReadFile(path)
	if err != nil || !json.Valid(bytes) {
		t.Fatal("config damaged")
	}
	runtimeLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(runtimeLog), "dpi: windivert initialized. capture is started.") < 2 {
		t.Fatal("both embedded DPI launches must report initialized WinDivert capture")
	}
	for _, issue := range []string{
		"WinDivert shutdown failed", "cancel WinDivert I/O", "close WinDivert handle",
		"WinDivert shutdown wait timed out", "WinDivert cleanup incomplete",
		"timed out waiting for DPI backend", "stop DPI backend:",
		"DPI backend unavailable", "DPI portable directories unavailable",
	} {
		if strings.Contains(string(runtimeLog), issue) {
			t.Fatalf("runtime log contains %q:\n%s", issue, runtimeLog)
		}
	}
	t.Log("runtime log: no shutdown/close/cleanup errors")
	ipv6.report.After = ipv6.probe(t, false)
	t.Log("IPv6 fixture after service: documentation and scoped link-local A/AAAA timed out")
}

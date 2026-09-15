//go:build windows

package main

import (
	"context"
	"encoding/json"
	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in only. Tests the real SCM with WinDivert off and restores the
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
	binary, err := filepath.Abs("SonDNS.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(binary); err != nil {
		t.Fatal("build SonDNS.exe before this test")
	}
	dir := t.TempDir()
	config := DefaultConfig()
	config.Interception = "off"
	config.LogFile = filepath.Join(dir, "system.log")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.Listen = ln.Addr().String()
	_ = ln.Close()
	config.Timeout = 3 * time.Second
	path := filepath.Join(dir, "config.json")
	checkGUIReadOnly := func() {
		t.Helper()
		before, err := queryServiceSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		configBefore, _ := os.ReadFile(path)
		gui := exec.Command(binary, "gui", "--config", path)
		if err = gui.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = gui.Process.Kill(); _ = gui.Wait() }()
		time.Sleep(500 * time.Millisecond)
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
		if err = SaveConfig(path, config); err != nil {
			t.Fatal(err)
		}
		checkGUIReadOnly()
		command("install", "--config", path)
		wait(svc.Running)
		snap, err := queryServiceSnapshot()
		if err != nil || snap.StartType != serviceStartType(auto) {
			t.Fatalf("wrong installed mode: %+v %v", snap, err)
		}
		t.Logf("installed: %s", snap.text())
		checkGUIReadOnly()
		if !auto {
			// A real local UDP and TCP query exercises the service and RAM log IPC.
			for _, network := range []string{"udp", "tcp"} {
				msg := new(dns.Msg)
				msg.SetQuestion("example.com.", dns.TypeA)
				reply, _, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).Exchange(msg, config.Listen)
				if err != nil {
					t.Fatal(err)
				}
				if reply == nil || !reply.Response {
					t.Fatal("no DNS response")
				}
			}
			var rows []DNSRequestEntry
			for n := 0; n < 20; n++ {
				response, readErr := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
				err = readErr
				rows = response.Entries
				if len(rows) >= 2 {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil || len(rows) < 2 {
				t.Fatalf("service didn't log both queries: %+v %v", rows, err)
			}
			t.Logf("DNS log: %s, %s", rows[0].Result, rows[1].Result)
			response, err := callDNSIPC(dnsIPCRequest{Op: "clear_log"})
			if err != nil || response.Version == 0 {
				t.Fatal(err)
			}
			response, err = callDNSIPC(dnsIPCRequest{Op: "snapshot"})
			if err != nil || len(response.Entries) != 0 {
				t.Fatal("clear didn't reset RAM log")
			}
			if err = setServiceAutoStart(true); err != nil {
				t.Fatal(err)
			}
			if err = restartService(); err != nil {
				t.Fatal(err)
			}
			wait(svc.Running)
			snap, err = queryServiceSnapshot()
			if err != nil || snap.StartType != windows.SERVICE_AUTO_START {
				t.Fatal("restart reset autostart")
			}
			if err = setServiceAutoStart(false); err != nil {
				t.Fatal(err)
			}
		}
		command("uninstall")
		snap, err = queryServiceSnapshot()
		if err != nil || snap.Exists {
			t.Fatalf("uninstall incomplete: %+v %v", snap, err)
		}
	}
	// Ensure saved config remains valid JSON after the full lifecycle.
	bytes, err := os.ReadFile(path)
	if err != nil || !json.Valid(bytes) {
		t.Fatal("config damaged")
	}
}

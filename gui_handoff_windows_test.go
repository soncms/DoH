//go:build windows

package main

import (
	"os"
	"os/exec"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGUIHandoffQuotesConfigAndWaitsForExit(t *testing.T) {
	config := `F:\Portable Apps\SonDNS\data\settings & saved.json`
	args := guiHandoffArguments(config, 1234)
	parsed, err := windows.DecomposeCommandLine(windows.ComposeCommandLine(append([]string{"SonDNS.exe"}, args...)))
	if err != nil || !reflect.DeepEqual(parsed[1:], args) {
		t.Fatalf("handoff arguments changed: %q %v", parsed, err)
	}
	if err := waitForGUIHandoff(uint32(os.Getpid())); err == nil {
		t.Fatal("self-wait accepted")
	}
	if err := waitForGUIHandoff(0); err != nil {
		t.Fatal(err)
	}
	// A real process handle, independent of GUI timers or a stale PID file.
	cmd := exec.Command(os.Args[0], "-test.run=^TestGUIHandoffChild$")
	cmd.Env = append(os.Environ(), "SONDNS_HANDOFF_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if err := waitForGUIHandoff(uint32(cmd.Process.Pid)); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestGUIHandoffChild(t *testing.T) {
	if os.Getenv("SONDNS_HANDOFF_CHILD") != "1" {
		t.Skip("process fixture")
	}
	time.Sleep(150 * time.Millisecond)
}

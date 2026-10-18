//go:build windows

package service

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestServiceRunConfigPaths(t *testing.T) {
	exe := `F:\Portable Apps\Renamed DNS.exe`
	root := filepath.Dir(exe)
	for _, args := range [][]string{
		{"run"},
		{"run", `--config=data\custom & saved.json`},
		{"run", "--config", `F:\Portable Apps\data\custom & saved.json`},
	} {
		command := windows.ComposeCommandLine(append([]string{exe}, args...))
		got, err := serviceCommandConfigPath(command, exe)
		want := filepath.Join(root, "data", "custom & saved.json")
		if len(args) == 1 {
			want = defaultConfigForExe(exe)
		}
		if err != nil || got != want {
			t.Fatalf("SCM command %q selected %q, want %q: %v", command, got, want, err)
		}
		// Exercise the exact entry parser used before svc.Run, independently
		// of the GUI's Windows command-line decomposition.
		actual, err := ServiceRunConfigPath(root, args)
		if err != nil || actual != got {
			t.Fatalf("runtime and GUI disagree: %q vs %q %v", actual, got, err)
		}
	}
	for _, args := range [][]string{
		nil, {"gui"}, {"run", "--config="}, {"run", "--config", ""},
		{"run", "--config=..\\outside.json"}, {"run", "--config=F:\\outside.json"},
		{"run", "--config=data.json:stream"}, {"run", "--config=NUL"},
		{"run", "--config=data.json", "--config=other.json"},
		{"run", "--config", "data.json", "--extra"}, {"run", "--unknown"},
		{"run", "--config=data\x00.json"},
	} {
		if _, err := ServiceRunConfigPath(root, args); err == nil {
			t.Fatalf("ambiguous/unsafe service args accepted: %q", args)
		}
	}
	if _, err := serviceCommandConfigPath(windows.ComposeCommandLine([]string{`F:\Another\DNS.exe`, "run"}), exe); err == nil {
		t.Fatal("foreign executable accepted")
	}
}

func TestServiceConfigBindingPreservesRecoveryOwnership(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(filepath.Dir(exe), "data", "custom saved.json")
	s := &testServiceConfig{value: mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config=" + config})}}
	if err := requireServiceConfigForExe(s, exe, config); err != nil {
		t.Fatal(err)
	}
	if err := requireServiceConfigForExe(s, exe, strings.ToUpper(config)); err != nil {
		t.Fatal(err)
	}
	if err := requireServiceConfigForExe(s, exe, defaultConfigForExe(exe)); err == nil || !strings.Contains(err.Error(), "different configuration") {
		t.Fatalf("wrong config accepted: %v", err)
	}
	// Stop/uninstall use EXE ownership so recovery remains possible even when
	// a GUI was opened with a different config or the SCM command is malformed.
	if err := requireThisService(s); err != nil {
		t.Fatalf("config guard blocked recovery: %v", err)
	}
	s.value.BinaryPathName = windows.ComposeCommandLine([]string{exe, "run", "--config=..\\outside.json"})
	if err := requireServiceConfigForExe(s, exe, config); err == nil {
		t.Fatal("unsafe binding accepted")
	}
	if err := requireThisService(s); err != nil {
		t.Fatalf("unsafe config blocked stop/uninstall ownership: %v", err)
	}
}

func TestTrayCommandBindsCustomPortableConfig(t *testing.T) {
	exe := `F:\Portable Apps\SonDNS.exe`
	config := `F:\Portable Apps\data\settings & café.json`
	command, err := guiTrayCommand(exe, config)
	if err != nil {
		t.Fatal(err)
	}
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || !reflect.DeepEqual(args, []string{exe, "gui", "--tray", "--config", config}) {
		t.Fatalf("tray command lost config/quoting: %q %v", args, err)
	}
	for _, bad := range []string{`..\outside.json`, `F:\outside.json`, `data\NUL`, `data\file.json:stream`, "data\x00.json"} {
		if _, err := guiTrayCommand(exe, bad); err == nil {
			t.Fatalf("unsafe tray config accepted: %q", bad)
		}
	}
	if _, err := guiTrayCommand("SonDNS.exe", config); err == nil {
		t.Fatal("relative tray EXE accepted")
	}
	long := `F:\` + strings.Repeat("folder", 30) + `\SonDNS.exe`
	if _, err := guiTrayCommand(long, ""); err == nil || !strings.Contains(err.Error(), "260-character") {
		t.Fatalf("overlong Run command accepted: %v", err)
	}
}

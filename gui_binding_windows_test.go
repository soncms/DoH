//go:build windows && !console

package main

import (
	"crypto/rand"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWrongConfigDoesNotSaveOrApplySettings(t *testing.T) {
	exe := `F:\SonDNS\SonDNS.exe`
	actual := `F:\SonDNS\data\active custom.json`
	s := &testServiceConfig{value: mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config=" + actual})}}
	query := func() (serviceSnapshot, error) {
		return serviceSnapshot{}, requireServiceConfigForExe(s, exe, defaultConfigForExe(exe))
	}
	previous := DefaultConfig()
	candidate := previous.Clone()
	candidate.ServiceAutoStart = true
	candidate.Upstreams = []Upstream{PredefinedUpstream(ProviderNextDNS)}
	save := func(Config) error { t.Fatal("mismatched GUI wrote a config"); return nil }
	change := func(bool) (func() error, error) { t.Fatal("mismatched GUI changed startup"); return nil, nil }
	call := func(dnsIPCRequest) (dnsIPCResponse, error) {
		t.Fatal("mismatched GUI sent a settings RPC")
		return dnsIPCResponse{}, nil
	}
	got, _, err := applyDNSProvider(previous, candidate, save, query, call)
	if err == nil || !reflect.DeepEqual(got, previous) {
		t.Fatalf("DNS mismatch was not rejected: %v", err)
	}
	got, err = applyStartWithWindows(previous, candidate, query, change, change, save)
	if err == nil || !reflect.DeepEqual(got, previous) {
		t.Fatalf("startup mismatch was not rejected: %v", err)
	}
}

func TestGUIImplicitConfigFollowsOwnedService(t *testing.T) {
	exe := `F:\Portable Apps\SonDNS.exe`
	custom := `F:\Portable Apps\data\custom & saved.json`
	for _, tc := range []struct {
		name    string
		binding *mgr.Config
		want    string
		warning bool
	}{
		{"absent", nil, defaultConfigForExe(exe), false},
		{"owned-custom", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config=" + custom})}, custom, false},
		{"owned-separated", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config", custom})}, custom, false},
		{"owned-default", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run"})}, defaultConfigForExe(exe), false},
		{"foreign", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{`F:\Other\SonDNS.exe`, "run", "--config=data.json"})}, defaultConfigForExe(exe), false},
		{"owned-unsafe", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config=..\\outside.json"})}, defaultConfigForExe(exe), true},
		{"owned-ambiguous", &mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{exe, "run", "--config=data.json", "--config=other.json"})}, defaultConfigForExe(exe), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := implicitGUIConfigAt(exe, tc.binding)
			if got != tc.want || (err != nil) != tc.warning {
				t.Fatalf("GUI picked %q, want %q, warning=%v: %v", got, tc.want, tc.warning, err)
			}
		})
	}
}

func TestTrayRegistryPreservesCustomConfigAndMigration(t *testing.T) {
	path := `Software\SonDNS-config-test-` + rand.Text()
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	exe := `F:\Portable Apps\SonDNS.exe`
	config := `F:\Portable Apps\data\custom & saved.json`
	if _, err := changeGUIAutostartAt(registry.CURRENT_USER, path, exe, config, true); err != nil {
		t.Fatal(err)
	}
	value, err := readMigrationTray(registry.CURRENT_USER, path)
	if err != nil {
		t.Fatal(err)
	}
	args, err := windows.DecomposeCommandLine(value.Value)
	if err != nil || len(args) != 5 || args[4] != config {
		t.Fatalf("Run lost custom config: %q %v", args, err)
	}
	if err := validateMigrationTray(value, exe, `G:\New\SonDNS.exe`); err != nil {
		t.Fatalf("current tray rejected for migration: %v", err)
	}
	newExe := `G:\New\SonDNS.exe`
	newConfig := `G:\New\data\imported custom.json`
	undo, err := changeMigrationTrayAt(registry.CURRENT_USER, path, value, newExe, newConfig, true)
	if err != nil {
		t.Fatal(err)
	}
	current, err := readMigrationTray(registry.CURRENT_USER, path)
	if err != nil {
		t.Fatal(err)
	}
	args, err = windows.DecomposeCommandLine(current.Value)
	if err != nil || !reflect.DeepEqual(args, []string{newExe, "gui", "--tray", "--config", newConfig}) {
		t.Fatalf("migration lost target config: %q %v", args, err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	got, err := readMigrationTray(registry.CURRENT_USER, path)
	if err != nil || !reflect.DeepEqual(got, value) {
		t.Fatal("migration rollback did not restore original config command")
	}
	// An invalid/overlong replacement must not alter an existing Run value.
	if _, err := changeGUIAutostartAt(registry.CURRENT_USER, path, exe, `..\outside.json`, true); err == nil {
		t.Fatal("unsafe command accepted")
	}
	got, err = readMigrationTray(registry.CURRENT_USER, path)
	if err != nil || !reflect.DeepEqual(got, value) {
		t.Fatal("rejected registration changed existing value")
	}
}

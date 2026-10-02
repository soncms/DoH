//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceBinaryPath(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string
	}{
		{`"C:\Program Files\SonDNS\SonDNS.exe" run --config=C:\ProgramData\SonDNS\sondns.json`, `C:\Program Files\SonDNS\SonDNS.exe`},
		{`C:\SonDNS\SonDNS.exe run --config=C:\ProgramData\SonDNS\sondns.json`, `C:\SonDNS\SonDNS.exe`},
		{"", ""},
	} {
		if got := serviceBinaryPath(tc.command); got != tc.want {
			t.Fatalf("serviceBinaryPath(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestPurgeSonDNSInstallKeepsExecutableAndRemovesData(t *testing.T) {
	programData := t.TempDir()
	t.Setenv("ProgramData", programData)
	appDir := filepath.Join(programData, "SonDNS")
	if err := os.MkdirAll(filepath.Join(appDir, "zapret2", "lua"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "sondns.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	installDir := t.TempDir()
	exe := filepath.Join(installDir, "SonDNS.exe")
	backup := exe + ".bak-20261003-000000"
	for _, path := range []string{exe, backup} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := purgeSonDNSInstall(exe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(appDir); !os.IsNotExist(err) {
		t.Fatalf("app data still exists: %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("installed executable was removed: %v", err)
	}
}

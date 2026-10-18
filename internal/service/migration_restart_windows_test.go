//go:build windows && !console

package service

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// These are inert files, never service processes or driver inputs.
func migrationRestartFiles(t *testing.T, root string) serviceMigrationPlan {
	t.Helper()
	p := serviceMigrationPlan{
		SourceExe: filepath.Join(root, "source", "SonDNS.exe"), SourceConfig: filepath.Join(root, "source", "data", "sondns.json"),
		TargetExe: filepath.Join(root, "target", "SonDNS.exe"), TargetConfig: filepath.Join(root, "target", "data", "sondns.json"),
	}
	for path, data := range map[string][]byte{p.SourceExe: []byte("MZreviewed source fixture"), p.SourceConfig: []byte("reviewed source config"), p.TargetExe: []byte("MZcandidate fixture")} {
		migrationTestWrite(t, path, data)
	}
	p.SourceExeHash = migrationHash(readLifecycleMigrationFile(t, p.SourceExe))
	p.SourceConfigHash = migrationHash(readLifecycleMigrationFile(t, p.SourceConfig))
	p.TargetExeHash = migrationHash(readLifecycleMigrationFile(t, p.TargetExe))
	return p
}

func TestMigrationSourceFilesPinReviewedBytes(t *testing.T) {
	p := migrationRestartFiles(t, t.TempDir())
	if err := os.Remove(p.TargetExe); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationSourceFiles(p); err != nil {
		t.Fatalf("unavailable candidate prevented reviewed source-file checks: %v", err)
	}
	for _, path := range []string{p.SourceExe, p.SourceConfig} {
		original := readLifecycleMigrationFile(t, path)
		migrationTestWrite(t, path, []byte("changed after review"))
		if err := validateMigrationSourceFiles(p); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("changed source file was accepted or not identified: %s: %v", path, err)
		}
		migrationTestWrite(t, path, original)
	}
}

func TestMigrationProtectedSourceRestartValidation(t *testing.T) {
	parent := os.Getenv("SONDNS_FILESYSTEM_TEST_PARENT")
	if parent == "" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an opted-in protected filesystem fixture")
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "SonDNS-restart-validation-test-"+rand.Text())
	if err := validateServiceParent(root); err != nil {
		t.Fatal(err)
	}
	if err := CreateServiceDirectory(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !insideRoot(parent, root) || !strings.HasPrefix(filepath.Base(root), "SonDNS-restart-validation-test-") || filepath.Clean(parent) == root {
			t.Error("unsafe restart fixture cleanup path")
			return
		}
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	for _, child := range []string{"source", "target"} {
		if err := CreateServiceDirectory(filepath.Join(root, child)); err != nil {
			t.Fatal(err)
		}
	}
	p := migrationRestartFiles(t, root)
	if err := nativeMigrationHooks(p).Validate(); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(p.TargetExe)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := nativeMigrationHooks(p).Validate(); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("negative control: full candidate validation was not blocked by the real lock: %v", err)
	}
	if err := nativeMigrationHooks(p).ValidateSource(); err != nil {
		t.Fatalf("safe reviewed source rejected while candidate was locked: %v", err)
	}
	// The narrower restart check still pins both source files.
	for _, path := range []string{p.SourceExe, p.SourceConfig} {
		original := readLifecycleMigrationFile(t, path)
		migrationTestWrite(t, path, []byte("changed protected source"))
		if err := nativeMigrationHooks(p).ValidateSource(); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("changed protected source accepted: %s: %v", path, err)
		}
		migrationTestWrite(t, path, original)
	}
	unsafe := migrationRestartFiles(t, t.TempDir())
	unsafe.TargetExe, unsafe.TargetExeHash = p.TargetExe, p.TargetExeHash
	if err := nativeMigrationHooks(unsafe).ValidateSource(); err == nil {
		t.Fatal("unsafe source folder accepted for SYSTEM restart")
	}
	t.Log("protected source restart validation is independent of the locked candidate; changed bytes and unsafe source remain rejected")
}

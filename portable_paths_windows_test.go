//go:build windows && !console

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortablePathsIgnoreWorkingDirectoryAndProfile(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(exe), "data", "sondns.json")
	t.Setenv("ProgramData", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Chdir(t.TempDir())
	if got := defaultConfigPath(); got != expected {
		t.Fatalf("config path depends on profile/cwd: %s", got)
	}
	root := t.TempDir()
	for _, configured := range []string{`C:\ProgramData\SonDNS\old.log`, `..\outside.log`, `C:relative.log`, `\\server\share\outside.log`} {
		logger, closeLog, err := newLoggerAt(root, Config{LogFile: configured})
		if err != nil {
			t.Fatal(err)
		}
		logger.Print("portable-path-test")
		closeLog()
	}
	if _, err := os.Stat(filepath.Join(root, "data", "sondns.log")); err != nil {
		t.Fatalf("legacy/escaping log paths were not kept local: %v", err)
	}
	if _, err := portableConfigPath(root, filepath.Join(t.TempDir(), "outside.json")); err == nil {
		t.Fatal("configuration escaped portable folder")
	}
	for _, path := range []string{`data\sondns.json:stream`, `C:relative.json`, `NUL`, "."} {
		if _, err := portableConfigPath(root, path); err == nil {
			t.Errorf("unsafe config path accepted: %s", path)
		}
	}
}

func TestPortableExternalDPIHelperStaysInsideAppFolder(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "helper.exe")
	other := filepath.Join(outside, "helper.exe")
	for _, path := range []string{inside, other} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(outside)
	if got := resolvePortableDPIExecutableAt(root, "helper.exe"); got != inside {
		t.Fatalf("helper resolved from cwd instead of the app folder: %s", got)
	}
	for _, path := range []string{other, `..\helper.exe`, "missing.exe", "helper.cmd"} {
		if got := resolvePortableDPIExecutableAt(root, path); got != "" {
			t.Errorf("external helper accepted: %s -> %s", path, got)
		}
	}
	link := filepath.Join(root, "linked.exe")
	if err := os.Symlink(other, link); err == nil {
		if got := resolvePortableDPIExecutableAt(root, link); got != "" {
			t.Fatal("portable helper followed a file link outside the folder")
		}
	}
}

func TestPortableHelperEnvironmentKeepsTemporaryFilesLocal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TEMP", t.TempDir())
	t.Setenv("TMP", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	env, err := portableHelperEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key == "TEMP" || key == "TMP" || key == "TMPDIR" || key == "HOME" {
			seen[key]++
			if !insideRoot(root, filepath.FromSlash(value)) {
				t.Fatalf("helper can write outside portable folder via %s", key)
			}
		}
	}
	for _, key := range []string{"TEMP", "TMP", "TMPDIR", "HOME"} {
		if seen[key] != 1 {
			t.Fatalf("ambiguous/missing helper variable %s: %d", key, seen[key])
		}
	}
}

func TestPortableEmbeddedExtractionAndRepair(t *testing.T) {
	root := t.TempDir()
	forbidden := t.TempDir()
	t.Setenv("ProgramData", forbidden)
	t.Setenv("APPDATA", forbidden)
	t.Setenv("LOCALAPPDATA", forbidden)
	dir, err := ensureEmbeddedZapret2At(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir != embeddedRuntimePath(root) {
		t.Fatalf("runtime escaped the portable folder: %s", dir)
	}
	for _, rel := range []string{"WinDivert.dll", "WinDivert64.sys", "winws2.exe", "cygwin1.dll", "zapret2/lua/zapret-lib.lua", "zapret2/lua/zapret-antidpi.lua"} {
		if _, err = os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing embedded runtime %s: %v", rel, err)
		}
	}
	for _, rel := range []string{"LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "licenses/modules.json", "licenses/go/github.com_AdguardTeam_dnsproxy/LICENSE", "licenses/runtime/cygwin/CYGWIN_LICENSE", "licenses/runtime/windivert/LICENSE"} {
		if _, err = os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("single EXE omitted license %s: %v", rel, err)
		}
	}
	dll := filepath.Join(dir, "WinDivert.dll")
	if err = os.WriteFile(dll, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = ensureEmbeddedZapret2At(root, nil); err != nil {
		t.Fatal(err)
	}
	if err = verifyWinDivertFiles(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(forbidden)
	if err != nil || len(entries) != 0 {
		t.Fatalf("extraction wrote profile files: entries=%v err=%v", entries, err)
	}
}

func TestPortableExtractionRejectsDirectoryLink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "runtime")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := ensureEmbeddedZapret2At(root, nil); err == nil {
		t.Fatal("runtime extraction followed a directory link")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("extraction changed linked directory: entries=%v err=%v", entries, err)
	}
}

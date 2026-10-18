//go:build windows && !console

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestMigrationCommandPaths(t *testing.T) {
	for _, args := range [][]string{
		{`F:\Old Folder\SonDNS.exe`, "run", `--config=F:\Old Folder\data\sondns.json`},
		{`F:\Old Folder\SonDNS.exe`, "run", "--config", `data\sondns.json`},
	} {
		exe, config, err := migrationSourcePaths(windows.ComposeCommandLine(args))
		if err != nil || exe != args[0] || config != `F:\Old Folder\data\sondns.json` {
			t.Fatalf("valid service rejected: %q %q %v", exe, config, err)
		}
	}
	for _, args := range [][]string{
		{`SonDNS.exe`, "run", "--config=data.json"},
		{`F:\Old\other.exe`, "run", "--config=data.json"},
		{`\\server\share\SonDNS.exe`, "run", "--config=data.json"},
		{`F:\Old\SonDNS.exe`, "gui", "--config=data.json"},
		{`F:\Old\SonDNS.exe`, "run", "--config=..\\outside.json"},
		{`F:\Old\SonDNS.exe`, "run", "--config=data.json:stream"},
		{`F:\Old\SonDNS.exe`, "run", "--config=NUL"},
		{`F:\Old\SonDNS.exe`, "run", "--config="},
		{`F:\Old\SonDNS.exe`, "run", "--config=data.json", "--extra"},
	} {
		if _, _, err := migrationSourcePaths(windows.ComposeCommandLine(args)); err == nil {
			t.Fatalf("unsafe command accepted: %q", args)
		}
	}
}

func migrationTestWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationBoundedReadAndConfigUndo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data", "sondns.json")
	migrationTestWrite(t, path, []byte("candidate"))
	if data, err := readMigrationFile(root, path, 9); err != nil || string(data) != "candidate" {
		t.Fatalf("valid read: %q %v", data, err)
	}
	if _, err := readMigrationFile(root, path, 8); err == nil {
		t.Fatal("size cap ignored")
	}
	if _, err := readMigrationFile(root, filepath.Join(root, "..", "outside"), 9); err == nil {
		t.Fatal("path escape accepted")
	}
	undo := migrationConfigUndo(root, path, []byte("original bytes\r\n"), true, migrationHash([]byte("candidate")))
	migrationTestWrite(t, path, []byte("concurrent"))
	if err := undo(); err == nil {
		t.Fatal("concurrent config overwritten")
	}
	migrationTestWrite(t, path, []byte("candidate"))
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo not idempotent: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original bytes\r\n" {
		t.Fatal("original bytes not preserved")
	}
	if err := os.Link(path, filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := readMigrationFile(root, path, 100); err == nil {
		t.Fatal("hardlink source accepted")
	}
	if err := os.Remove(filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "data"), filepath.Join(root, "junction")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readMigrationFile(root, filepath.Join(root, "junction", "sondns.json"), 100); err == nil {
		t.Fatal("source ancestor reparse accepted")
	}
}

func TestMigrationTraySnapshotAndRollback(t *testing.T) {
	path := `Software\SonDNS-migration-test-` + rand.Text()
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	old := migrationTrayValue{true, registry.EXPAND_SZ, `"%SystemDrive%\Old Folder\SonDNS.exe" gui --tray`}
	if err := writeMigrationTray(registry.CURRENT_USER, path, old); err != nil {
		t.Fatal(err)
	}
	expanded, _ := registry.ExpandString(`%SystemDrive%\Old Folder\SonDNS.exe`)
	if err := validateMigrationTray(old, expanded, `F:\New\SonDNS.exe`); err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationTray(old, `F:\Other\SonDNS.exe`, `F:\New\SonDNS.exe`); err == nil {
		t.Fatal("unrelated tray accepted")
	}
	undo, err := changeMigrationTrayAt(registry.CURRENT_USER, path, old, `F:\New Folder\SonDNS.exe`, "", true)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := readMigrationTray(registry.CURRENT_USER, path)
	if err != nil || applied.Kind != registry.SZ || !strings.Contains(applied.Value, "New Folder") {
		t.Fatalf("candidate not registered: %+v %v", applied, err)
	}
	newer := migrationTrayValue{true, registry.SZ, "external admin"}
	if err := writeMigrationTray(registry.CURRENT_USER, path, newer); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err == nil {
		t.Fatal("concurrent tray change overwritten")
	}
	if err := writeMigrationTray(registry.CURRENT_USER, path, applied); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if got, err := readMigrationTray(registry.CURRENT_USER, path); err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("type/raw value not restored: %+v %v", got, err)
	}
	if _, err := changeMigrationTrayAt(registry.CURRENT_USER, path, migrationTrayValue{}, "unused", "", false); err == nil {
		t.Fatal("stale snapshot accepted")
	}
	undo, err = changeMigrationTrayAt(registry.CURRENT_USER, path, old, "unused", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationProtectedBackupAndSettings(t *testing.T) {
	parent := os.Getenv("SONDNS_FILESYSTEM_TEST_PARENT")
	if parent == "" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("set SONDNS_FILESYSTEM_TEST_PARENT to a trusted fixed-disk parent and run elevated")
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "SonDNS-migration-test-"+rand.Text())
	if err := validateServiceParent(destination); err != nil {
		t.Fatal(err)
	}
	if err := createServiceDirectory(destination); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !insideRoot(parent, destination) || !strings.HasPrefix(filepath.Base(destination), "SonDNS-migration-test-") || filepath.Clean(parent) == filepath.Clean(destination) {
			t.Error("unsafe cleanup")
			return
		}
		if err := os.RemoveAll(destination); err != nil {
			t.Error(err)
		}
	})
	target := filepath.Join(destination, "SonDNS.exe")
	migrationTestWrite(t, target, []byte("MZtarget fixture; never executed"))
	source := t.TempDir()
	oldExe := filepath.Join(source, "SonDNS.exe")
	oldConfig := filepath.Join(source, "data", "sondns.json")
	migrationTestWrite(t, oldExe, []byte("MZlegacy fixture; never executed"))
	c := DefaultConfig()
	c.Upstreams = []Upstream{{URL: "https://resolver.example/my-profile", DNSSEC: true}}
	c.DPI.CaptureMode = "tcp+udp"
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	c.LogFile = `C:\ProgramData\Old\log.txt`
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	migrationTestWrite(t, oldConfig, raw)
	migrationTestWrite(t, filepath.Join(source, "runtime", "old.exe"), []byte("legacy helper; never executed"))
	previous := mgr.Config{BinaryPathName: windows.ComposeCommandLine([]string{oldExe, "run", "--config=" + oldConfig}), ServiceType: windows.SERVICE_WIN32_OWN_PROCESS, StartType: windows.SERVICE_AUTO_START, ServiceStartName: "LocalSystem"}
	configPath := filepath.Join(destination, "data", "sondns.json")
	p, err := prepareServiceMigration(previous, svc.Running, target, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Settings.Upstreams, c.Upstreams) || p.Settings.DPI.CaptureMode != c.DPI.CaptureMode || p.Settings.LogFile != defaultLogPath() || !p.Settings.ServiceAutoStart || p.CanRestartSource || p.RecoveryLimit == "" {
		t.Fatalf("settings/recovery limits wrong: %+v", p)
	}
	stage, err := stageServiceMigration(p)
	if err != nil {
		t.Fatal(err)
	}
	requireMigrationRecoveryMetadata(t, stage.Directory, configPath, nil, false)
	data, err := os.ReadFile(filepath.Join(stage.Directory, "legacy", "SonDNS.exe.bin"))
	if err != nil || migrationHash(data) != p.SourceExeHash {
		t.Fatal("legacy EXE backup missing or changed")
	}
	if _, err := os.Stat(filepath.Join(stage.Directory, "legacy", "runtime", "old.exe.bin")); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(configPath)
	if err != nil || !reflect.DeepEqual(got.Upstreams, c.Upstreams) {
		t.Fatalf("candidate config: %+v %v", got, err)
	}
	if err := validatePortableServiceFolder(destination); err != nil {
		t.Fatal(err)
	}
	if err := stage.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("absent destination config not restored")
	}
	// Check the exact prior bytes, and fail safely when the reviewed source changes.
	migrationTestWrite(t, configPath, []byte("original destination bytes"))
	stage, err = stageServiceMigration(p)
	if err != nil {
		t.Fatal(err)
	}
	requireMigrationRecoveryMetadata(t, stage.Directory, configPath, []byte("original destination bytes"), true)
	if err := stage.Undo(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(configPath)
	if string(data) != "original destination bytes" {
		t.Fatal("destination bytes not restored")
	}
	// An empty previous file is distinct from a config that did not exist.
	migrationTestWrite(t, configPath, nil)
	stage, err = stageServiceMigration(p)
	if err != nil {
		t.Fatal(err)
	}
	requireMigrationRecoveryMetadata(t, stage.Directory, configPath, nil, true)
	if err := stage.Undo(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(configPath)
	if err != nil || len(data) != 0 {
		t.Fatalf("empty destination config was not restored: %v", err)
	}
	migrationTestWrite(t, configPath, []byte("original destination bytes"))
	migrationTestWrite(t, oldConfig, []byte("changed source"))
	if _, err := stageServiceMigration(p); err == nil {
		t.Fatal("changed source was staged")
	}
	data, _ = os.ReadFile(configPath)
	if string(data) != "original destination bytes" {
		t.Fatal("failed staging modified destination config")
	}
	// The importer refuses legacy executable/argument customization.
	c.DPI.Arguments = []string{"--custom"}
	raw, _ = json.Marshal(c)
	migrationTestWrite(t, oldConfig, raw)
	if _, err := prepareServiceMigration(previous, svc.Running, target, configPath); err == nil {
		t.Fatal("custom legacy helper arguments imported")
	}
}

func requireMigrationRecoveryMetadata(t *testing.T, directory, candidatePath string, original []byte, existed bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal migrationJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(directory, "files.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	r := journal.Recovery
	if journal.SchemaVersion != 1 || journal.Status != "prepared" || !r.Ready || r.FilesManifestSHA256 != migrationHash(manifest) ||
		r.DestinationExisted != existed || r.CandidateSHA256 != migrationHash(candidate) || r.CandidateSize != len(candidate) {
		t.Fatalf("recovery metadata missing or inconsistent: %+v", journal)
	}
	backup, err := os.ReadFile(filepath.Join(directory, "destination-config.json.bin"))
	if existed {
		if err != nil || !bytes.Equal(backup, original) || r.DestinationSHA256 != migrationHash(original) || r.DestinationSize != len(original) {
			t.Fatalf("previous destination metadata/backup incorrect: %+v error %v", r, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) || r.DestinationSHA256 != "" || r.DestinationSize != 0 {
		t.Fatal("absent destination was recorded as an empty existing file")
	}
}

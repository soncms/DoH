//go:build windows && !console

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReadOnlyGUIKeepsMonitoringAfterAnUpgrade(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an elevated fixture creator")
	}
	root := filepath.Join(t.TempDir(), "protected")
	if err := createServiceDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := ensurePortableNoticesAt(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "PORTABLE.md")
	if err := os.WriteFile(path, []byte("older extracted documentation"), 0o644); err != nil {
		t.Fatal(err)
	}
	withRestrictedServiceTestToken(t, func() {
		refresh, err := prepareGUIPortableNotices(root, false)
		if err != nil || !refresh {
			t.Fatalf("read-only upgrade blocked monitoring: refresh=%v err=%v", refresh, err)
		}
	})
	refresh, err := prepareGUIPortableNotices(root, true)
	if err != nil || refresh {
		t.Fatalf("administrator failed to refresh documents: %v %v", refresh, err)
	}
	want, _ := portableNotices.ReadFile("PORTABLE.md")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("new embedded documentation was not restored")
	}
}

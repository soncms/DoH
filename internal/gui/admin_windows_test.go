//go:build windows && !console

package gui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
	resources "sondns"
	"sondns/internal/service"
	"sondns/internal/testutil"
)

func TestReadOnlyGUIKeepsMonitoringAfterAnUpgrade(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("requires an elevated fixture creator")
	}
	root := filepath.Join(t.TempDir(), "protected")
	if err := service.CreateServiceDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := service.EnsurePortableNoticesAt(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "PORTABLE.md")
	if err := os.WriteFile(path, []byte("older extracted documentation"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.WithRestrictedServiceTestToken(t, func() {
		refresh, err := prepareGUIPortableNotices(root, false)
		if err != nil || !refresh {
			t.Fatalf("read-only upgrade blocked monitoring: refresh=%v err=%v", refresh, err)
		}
	})
	refresh, err := prepareGUIPortableNotices(root, true)
	if err != nil || refresh {
		t.Fatalf("administrator failed to refresh documents: %v %v", refresh, err)
	}
	want, _ := resources.PortableNotices.ReadFile("PORTABLE.md")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("new embedded documentation was not restored")
	}
}

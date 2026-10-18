//go:build windows

package main

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestServiceACLPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, sddl string
		mode       serviceACLMode
		allowed    bool
	}{
		{"protected", portableServiceSDDL, serviceACLDirectory, true},
		{"user_writable", "O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FW;;;BU)", serviceACLFile, false},
		{"world_writable", "O:BAD:P(A;;GA;;;WD)", serviceACLFile, false},
		{"user_owned", "O:S-1-5-21-1-2-3-1000D:P(A;;FA;;;SY)(A;;FR;;;BU)", serviceACLDirectory, false},
		{"null_dacl", "O:BAD:NO_ACCESS_CONTROL", serviceACLDirectory, false},
		{"parent_delete_child", "O:BAD:P(A;;0x40;;;BU)", serviceACLAncestor, false},
		{"parent_change_owner", "O:BAD:P(A;;WO;;;BU)", serviceACLAncestor, false},
		{"future_files_writable", "O:BAD:P(A;;FA;;;SY)(A;OICIIO;FW;;;BU)", serviceACLDirectory, false},
		{"volume_child_creation", "O:SYD:P(A;;FA;;;SY)(A;;0x1201bf;;;AU)", serviceACLVolume, true},
		{"volume_delete_child", "O:SYD:P(A;;0x40;;;AU)", serviceACLVolume, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			if err != nil {
				t.Fatal(err)
			}
			err = validateServiceACL(sd, tc.mode)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}

func TestServiceFolderRejectsUntrustedParentsAndLinks(t *testing.T) {
	if err := validateServiceParent(`\\server\share\SonDNS`); err == nil {
		t.Fatal("network service folder accepted")
	}
	if err := validateServiceParent(filepath.VolumeName(portableRoot()) + `\`); err == nil {
		t.Fatal("disk root accepted as app folder")
	}
	root, outside := t.TempDir(), t.TempDir()
	file := filepath.Join(root, "shared.dll")
	if err := os.WriteFile(file, []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(outside, "shared.dll")); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceObject(file, serviceACLFile); err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("hard-linked service file accepted: %v", err)
	}
	link := filepath.Join(root, "runtime")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateServiceObject(link, serviceACLDirectory); err == nil {
		t.Fatal("service folder followed a junction/symlink")
	}
}

func TestSecurePortableCopyAndRestrictedUser(t *testing.T) {
	parent := os.Getenv("SONDNS_FILESYSTEM_TEST_PARENT")
	if parent == "" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("set SONDNS_FILESYSTEM_TEST_PARENT to a trusted fixed-disk parent and run elevated")
	}
	parent, err := filepath.Abs(parent)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "SonDNS-test-"+rand.Text())
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Upstreams = []Upstream{{URL: "https://resolver.example/dns-query", DNSSEC: true}}
	target, err := createSecurePortableCopy(source, destination, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !insideRoot(parent, destination) || !strings.HasPrefix(filepath.Base(destination), "SonDNS-test-") || filepath.Clean(destination) == filepath.Clean(parent) {
			t.Error("unsafe fixture cleanup path")
			return
		}
		if err := os.RemoveAll(destination); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
	})
	configPath := filepath.Join(destination, "data", "sondns.json")
	config, err := LoadConfig(configPath)
	if err != nil || config.Upstreams[0].URL != c.Upstreams[0].URL || !config.Upstreams[0].DNSSEC {
		t.Fatalf("copied settings changed: %+v %v", config, err)
	}
	if _, err := createSecurePortableCopy(source, destination, DefaultConfig()); err == nil {
		t.Fatal("existing destination was overwritten")
	}
	config, _ = LoadConfig(configPath)
	if config.Upstreams[0].URL != c.Upstreams[0].URL {
		t.Fatal("failed copy changed existing settings")
	}
	withRestrictedServiceTestToken(t, func() {
		if _, err := os.ReadFile(configPath); err != nil {
			t.Fatalf("ordinary desktop user cannot read config: %v", err)
		}
		for _, path := range []string{target, configPath, filepath.Join(destination, "injected.dll"), filepath.Join(destination, "data", "injected.json")} {
			if err := os.WriteFile(path, []byte("untrusted replacement"), 0o644); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Errorf("restricted write to %s: %v", path, err)
			}
		}
		if err := os.Rename(destination, destination+"-moved"); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("restricted rename of service folder: %v", err)
		}
		if err := os.Remove(target); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("restricted delete of EXE: %v", err)
		}
	})
	if err := validatePortableServiceFolder(destination); err != nil {
		t.Fatal(err)
	}
	// A protected folder inside a writable ancestor is still unsafe.
	unsafeParent := filepath.Join(destination, "writable-parent")
	if err := os.Mkdir(unsafeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FW;;;BU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(unsafeParent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceParent(filepath.Join(unsafeParent, "another-copy")); err == nil {
		t.Fatal("user-writable ancestor accepted")
	}
}

func withRestrictedServiceTestToken(t *testing.T, body func()) {
	t.Helper()
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &source); err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	disabled := windows.SIDAndAttributes{Sid: admins}
	var restricted windows.Token
	create := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")
	ok, _, callErr := create.Call(uintptr(source), 1, 1, uintptr(unsafe.Pointer(&disabled)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		t.Fatal(callErr)
	}
	defer restricted.Close()
	var token windows.Token
	if err := windows.DuplicateTokenEx(restricted, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE, nil, windows.SecurityImpersonation, windows.TokenImpersonation, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.SetThreadToken(nil, token); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			panic(err)
		}
	}()
	body()
}

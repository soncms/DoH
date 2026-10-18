//go:build windows && console

package service

import (
	"crypto/sha256"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	appconfig "sondns/internal/config"
	dnsengine "sondns/internal/engine"
)

const ConsoleBuild = true

func installationID(root string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root)))))[:16]
}

func consolePipe(root string, preview bool) string {
	suffix := "control"
	if preview {
		suffix = "preview"
	}
	return `\\.\pipe\SonDNS\` + installationID(root) + `\` + suffix
}

func syncGUIAutostart(string, bool) error { return nil }

func newRuntimeEngine(c appconfig.Config, logger *log.Logger) (runtimeEngine, *dnsengine.Resolver, error) {
	lock, err := loadComponents(PortableRoot())
	if err != nil {
		return nil, nil, err
	}
	e := newConsoleEngine(c, PortableRoot(), lock, logger)
	return e, &dnsengine.Resolver{Engine: e, Requests: e.ring}, nil
}

func portableRuntimeDir() (string, error) {
	lock, err := loadComponents(PortableRoot())
	if err != nil {
		return "", err
	}
	if err = verifyInstalled(PortableRoot(), lock); err != nil {
		return "", err
	}
	return componentPath(PortableRoot(), lock.Components["zapret2"])
}

func verifyDriverSignature(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	info := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: p}
	data := windows.WinTrustData{Size: uint32(unsafe.Sizeof(windows.WinTrustData{})), UIChoice: windows.WTD_UI_NONE, RevocationChecks: windows.WTD_REVOKE_NONE, UnionChoice: windows.WTD_CHOICE_FILE, StateAction: windows.WTD_STATEACTION_VERIFY, FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&info)}
	err = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &data)
	return err
}

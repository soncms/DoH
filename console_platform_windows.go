//go:build windows && console

package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const consoleBuild = true

func reportStartupError(err error) { fmt.Fprintln(os.Stderr, "error:", err) }

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
func runGUI() error                              { return fmt.Errorf("this build uses the CMD menu") }
func runGUIWithConfigAndTray(string, bool) error { return runGUI() }
func syncGUIAutostart(string, bool) error        { return nil }
func newRuntimeEngine(c Config, logger *log.Logger) (runtimeEngine, *Resolver, error) {
	lock, err := loadComponents(portableRoot())
	if err != nil {
		return nil, nil, err
	}
	e := newConsoleEngine(c, portableRoot(), lock, logger)
	return e, &Resolver{engine: e, requests: e.ring}, nil
}
func portableRuntimeDir() (string, error) {
	lock, err := loadComponents(portableRoot())
	if err != nil {
		return "", err
	}
	if err = verifyInstalled(portableRoot(), lock); err != nil {
		return "", err
	}
	return componentPath(portableRoot(), lock.Components["zapret2"])
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

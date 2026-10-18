//go:build windows && !console

package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lxn/walk"
	"golang.org/x/sys/windows"
	appconfig "sondns/internal/config"
	"sondns/internal/service"
)

// Relaunch only after UAC succeeds. The new window waits for this process to
// release the GUI mutex; cancellation leaves the original window open.
func (g *guiState) requireAdminGUI() bool {
	if windows.GetCurrentProcessToken().IsElevated() {
		return true
	}
	if g.busy {
		return false
	}
	exe, err := os.Executable()
	if err == nil {
		err = service.LaunchPortableGUI(exe, g.configPath, true, uint32(os.Getpid()))
	}
	if errors.Is(err, windows.ERROR_CANCELLED) {
		g.setStatus("Administrator access cancelled", false)
	} else if err != nil {
		g.setStatus("Could not open administrator window: "+err.Error(), true)
	} else {
		g.exiting = true
		_ = g.mw.Close()
	}
	return false
}

func prepareGUIPortableNotices(root string, admin bool) (bool, error) {
	err := service.EnsurePortableNoticesAt(root)
	if !admin && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		// An administrator may have replaced the EXE without refreshing older
		// extracted documents. Keep monitoring available until Manage updates it.
		return true, nil
	}
	return false, err
}

func (g *guiState) offerSecurePortableCopy(c appconfig.Config, reason error) {
	if !g.requireAdminGUI() {
		return
	}
	message := "Choose a location for a new portable folder. Your settings will be copied there; the current service will stay unchanged."
	if reason != nil {
		message = "The Windows service needs a protected portable folder.\n" + message + "\n\nCurrent folder: " + service.PortableRoot() + "\n" + reason.Error()
	}
	if walk.MsgBox(g.mw, "Choose portable location", message, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	dlg := walk.FileDialog{Title: "Choose the parent location for a new SonDNS folder"}
	accepted, err := dlg.ShowBrowseFolder(g.mw)
	if err != nil {
		g.setStatus(err.Error(), true)
		return
	}
	if !accepted {
		return
	}
	destination := filepath.Join(dlg.FilePath, "SonDNS")
	for suffix := 2; ; suffix++ {
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			g.setStatus(err.Error(), true)
			return
		}
		if suffix > 100 {
			g.setStatus("Choose a location with an unused SonDNS folder name", true)
			return
		}
		destination = filepath.Join(dlg.FilePath, fmt.Sprintf("SonDNS-%d", suffix))
	}
	if walk.MsgBox(g.mw, "Create portable copy", "Create and open SonDNS here?\n\n"+destination, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion|walk.MsgBoxDefButton2) != walk.DlgCmdYes {
		return
	}
	var copied string
	g.runAction("Creating portable copy…", func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		copied, err = service.CreateSecurePortableCopy(exe, destination, c)
		return "Portable copy created: " + destination, err
	}, func() {
		if err := service.LaunchPortableGUI(copied, filepath.Join(destination, "data", "sondns.json"), false, uint32(os.Getpid())); err != nil {
			g.setStatus("Copy created; open "+copied+": "+err.Error(), true)
			return
		}
		g.exiting = true
		_ = g.mw.Close()
	})
}

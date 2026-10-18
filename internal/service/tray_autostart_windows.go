//go:build windows && !console

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	appconfig "sondns/internal/config"
)

const runRegistryKey = `Software\Microsoft\Windows\CurrentVersion\Run`

const runRegistryName = "SonDNS"

func ApplyStartWithWindows(previous, candidate appconfig.Config, query func() (ServiceSnapshot, error), changeService, changeTray func(bool) (func() error, error), save func(appconfig.Config) error) (appconfig.Config, error) {
	state, err := query()
	if err != nil {
		return previous, fmt.Errorf("check SonDNS service before changing startup: %w", err)
	}
	var undoService, undoTray func() error
	if state.Exists && state.StartType != serviceStartType(candidate.ServiceAutoStart) {
		undoService, err = changeService(candidate.ServiceAutoStart)
	}
	if err == nil {
		undoTray, err = changeTray(candidate.ServiceAutoStart)
	}
	if err == nil {
		err = save(candidate)
	}
	if err == nil {
		return candidate, nil
	}
	// Roll back only operations that actually succeeded. In particular, a
	// foreign-service/query failure must not delete somebody else's Run entry.
	if undoTray != nil {
		err = errors.Join(err, undoTray())
	}
	if undoService != nil {
		err = errors.Join(err, undoService())
	}
	return previous, err
}

func syncGUIAutostart(config string, enable bool) error {
	_, err := ChangeGUIAutostart(config, enable)
	return err
}

func ChangeGUIAutostart(config string, enable bool) (func() error, error) {
	if enable {
		if err := ValidatePortableServiceFolder(PortableRoot()); err != nil {
			return nil, err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return changeGUIAutostartAt(registry.LOCAL_MACHINE, runRegistryKey, exe, config, enable)
}

func syncGUIAutostartAt(root registry.Key, keyPath, exe, config string, enable bool) error {
	_, err := changeGUIAutostartAt(root, keyPath, exe, config, enable)
	return err
}

// Service/tray startup is machine-wide. Never use the credential provider's
// HKCU when UAC runs SonDNS under another administrator account.
func changeGUIAutostartAt(root registry.Key, keyPath, exe, config string, enable bool) (func() error, error) {
	access := uint32(registry.QUERY_VALUE | registry.SET_VALUE | registry.WOW64_64KEY)
	var k registry.Key
	var err error
	if enable {
		k, _, err = registry.CreateKey(root, keyPath, access)
	} else {
		k, err = registry.OpenKey(root, keyPath, access)
		if errors.Is(err, registry.ErrNotExist) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, err
	}
	defer k.Close()
	previous, kind, err := k.GetStringValue(runRegistryName)
	existed := err == nil
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return nil, err
	}
	if existed {
		command := previous
		if kind == registry.EXPAND_SZ {
			command, err = registry.ExpandString(command)
			if err != nil {
				return nil, err
			}
		}
		parts, err := windows.DecomposeCommandLine(command)
		if err != nil || len(parts) == 0 || !strings.EqualFold(filepath.Clean(parts[0]), filepath.Clean(exe)) {
			return nil, errors.New("SonDNS tray startup belongs to another folder; manage it from its own SonDNS.exe")
		}
	}
	applied := ""
	if enable {
		applied, err = guiTrayCommand(exe, config)
		if err != nil {
			return nil, err
		}
		err = k.SetStringValue(runRegistryName, applied)
	} else if existed {
		err = k.DeleteValue(runRegistryName)
	} else {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return func() error {
		key, _, err := registry.CreateKey(root, keyPath, access)
		if err != nil {
			return err
		}
		defer key.Close()
		current, currentKind, readErr := key.GetStringValue(runRegistryName)
		matches := enable && readErr == nil && current == applied && currentKind == registry.SZ || !enable && errors.Is(readErr, registry.ErrNotExist)
		if !matches {
			return errors.New("tray startup changed concurrently; previous value was not restored")
		}
		if !existed {
			err = key.DeleteValue(runRegistryName)
			if errors.Is(err, registry.ErrNotExist) {
				return nil
			}
			return err
		}
		if kind == registry.EXPAND_SZ {
			return key.SetExpandStringValue(runRegistryName, previous)
		}
		return key.SetStringValue(runRegistryName, previous)
	}, nil
}

func ChangeServiceAutoStart(config string, enable bool) (func() error, error) {
	if enable {
		if err := ValidatePortableServiceFolder(PortableRoot()); err != nil {
			return nil, err
		}
	}
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := requireServiceConfigForExe(s, exe, config); err != nil {
		return nil, err
	}
	change, err := applyServiceStartupChange(scmStartupController{s}, enable)
	if err != nil || change == nil {
		return nil, err
	}
	return func() error {
		service, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_CHANGE_CONFIG)
		if err != nil {
			return err
		}
		defer service.Close()
		return change.rollback(scmStartupController{service})
	}, nil
}

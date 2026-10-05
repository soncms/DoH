//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const runRegistryKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runRegistryName = "SonDNS"

func uiExecutablePath() string {
	if override := os.Getenv("SONDNS_UI_PATH"); override != "" {
		return override
	}
	exe, err := os.Executable()
	if err != nil {
		return "SonDNS.exe"
	}
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, "SonDNS.exe"),
		filepath.Join(dir, "..", "SonDNS.exe"),
	}
	for _, candidate := range candidates {
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
	}
	return candidates[0]
}

func readGUIAutostart() (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(runRegistryName)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func writeGUIAutostart(value string, present bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runRegistryKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		if present && errors.Is(err, registry.ErrNotExist) {
			k, _, err = registry.CreateKey(registry.CURRENT_USER, runRegistryKey, registry.QUERY_VALUE|registry.SET_VALUE)
		}
	}
	if err != nil {
		return err
	}
	defer k.Close()
	if !present {
		err = k.DeleteValue(runRegistryName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	return k.SetStringValue(runRegistryName, value)
}

func syncGUIAutostart(enable bool) error {
	if enable {
		return writeGUIAutostart(fmt.Sprintf(`"%s" --tray`, uiExecutablePath()), true)
	}
	return writeGUIAutostart("", false)
}

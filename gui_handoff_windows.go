//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

func guiHandoffArguments(config string, previousPID uint32) []string {
	return []string{"gui", "--config", config, "--wait-for", strconv.FormatUint(uint64(previousPID), 10)}
}

func launchPortableGUI(exe, config string, elevate bool, previousPID uint32) error {
	verb := "open"
	if elevate {
		verb = "runas"
	}
	v, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	f, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	args, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(guiHandoffArguments(config, previousPID)))
	if err != nil {
		return err
	}
	cwd, err := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, v, f, args, cwd, windows.SW_SHOWNORMAL)
}

func waitForGUIHandoff(pid uint32) error {
	if pid == 0 {
		return nil
	}
	if pid == uint32(os.Getpid()) {
		return errors.New("cannot wait for the current process")
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil // The previous window has already exited.
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 15_000)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("previous SonDNS window did not exit (wait status %d)", state)
	}
	return nil
}

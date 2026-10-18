//go:build windows && !console

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// An implicit GUI launch follows an owned service's saved config. A foreign
// service remains read-only until the separate confirmed migration action.
func implicitGUIConfigAt(exe string, binding *mgr.Config) (string, error) {
	fallback := defaultConfigForExe(exe)
	if binding == nil || !strings.EqualFold(filepath.Clean(serviceBinaryPath(binding.BinaryPathName)), filepath.Clean(exe)) {
		return fallback, nil
	}
	path, err := serviceCommandConfigPath(binding.BinaryPathName, exe)
	if err != nil {
		return fallback, err
	}
	return path, nil
}

func implicitGUIConfig() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	fallback := defaultConfigForExe(exe)
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return fallback, err
	}
	return implicitGUIConfigAt(exe, &c)
}

func queryServiceSnapshotForConfig(config string) (serviceSnapshot, error) {
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return serviceSnapshot{}, nil
	}
	if err != nil {
		return serviceSnapshot{}, err
	}
	defer s.Close()
	exe, err := os.Executable()
	if err != nil {
		return serviceSnapshot{}, err
	}
	if err := requireServiceConfigForExe(s, exe, config); err != nil {
		return serviceSnapshot{}, err
	}
	st, err := s.Query()
	if err != nil {
		return serviceSnapshot{}, err
	}
	c, err := s.Config()
	if err != nil {
		return serviceSnapshot{}, err
	}
	if err := requireServiceCommandConfig(c.BinaryPathName, exe, config); err != nil {
		return serviceSnapshot{}, err
	}
	return serviceSnapshot{Exists: true, State: st.State, StartType: c.StartType}, nil
}

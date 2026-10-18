//go:build windows

package main

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
	"os"
	"path/filepath"
	"strings"
)

type serviceSnapshot struct {
	Exists    bool
	State     svc.State
	StartType uint32
}

func (s serviceSnapshot) autoStart() bool { return s.StartType == windows.SERVICE_AUTO_START }
func (s serviceSnapshot) pending() bool {
	return s.Exists && (s.State == svc.StartPending || s.State == svc.StopPending || s.State == svc.ContinuePending || s.State == svc.PausePending)
}
func (s serviceSnapshot) text() string {
	if !s.Exists {
		return "not installed"
	}
	switch s.State {
	case svc.StartPending, svc.ContinuePending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Stopped:
		return "stopped"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	case svc.Running:
		mode := "manual"
		if s.autoStart() {
			mode = "automatic"
		} else if s.StartType == windows.SERVICE_DISABLED {
			mode = "disabled"
		}
		return "running · " + mode
	default:
		return "unknown"
	}
}

// Monitoring uses query rights only, so opening a GUI without elevation can
// read the real state instead of misreporting Access Denied as Not Installed.
func openSonDNSService(access uint32) (*mgr.Service, error) {
	mutates := access&(windows.SERVICE_START|windows.SERVICE_STOP|windows.SERVICE_CHANGE_CONFIG|windows.DELETE) != 0
	if mutates {
		access |= windows.SERVICE_QUERY_CONFIG
	}
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(h)
	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return nil, err
	}
	sh, err := windows.OpenService(h, name, access)
	if err != nil {
		return nil, err
	}
	s := &mgr.Service{Name: serviceName, Handle: sh}
	if mutates {
		if err := requireThisService(s); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

func requireThisService(s serviceConfigurer) error {
	c, err := s.Config()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(serviceBinaryPath(c.BinaryPathName)), filepath.Clean(exe)) {
		return fmt.Errorf("SonDNS service belongs to another folder: %s; manage it from its own SonDNS.exe", c.BinaryPathName)
	}
	return nil
}

func queryServiceSnapshot() (serviceSnapshot, error) {
	s, err := openSonDNSService(windows.SERVICE_QUERY_STATUS | windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return serviceSnapshot{}, nil
	}
	if err != nil {
		return serviceSnapshot{}, err
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return serviceSnapshot{}, err
	}
	c, err := s.Config()
	if err != nil {
		return serviceSnapshot{}, err
	}
	return serviceSnapshot{Exists: true, State: st.State, StartType: c.StartType}, nil
}

func queryThisServiceSnapshot() (serviceSnapshot, error) {
	state, err := queryServiceSnapshot()
	if err != nil || !state.Exists {
		return state, err
	}
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return state, err
	}
	defer s.Close()
	return state, requireThisService(s)
}

func serviceStartType(auto bool) uint32 {
	if auto {
		return windows.SERVICE_AUTO_START
	}
	return windows.SERVICE_DEMAND_START
}

type serviceConfigurer interface {
	Config() (mgr.Config, error)
}

func setServiceAutoStart(auto bool) error {
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return err
	}
	defer s.Close()
	if auto && !consoleBuild {
		if err := validatePortableServiceFolder(portableRoot()); err != nil {
			return err
		}
	}
	return updateServiceStartup(scmStartupController{s}, auto)
}

func startInstalledService() error {
	s, err := openSonDNSService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer s.Close()
	if !consoleBuild {
		if err := validatePortableServiceFolder(portableRoot()); err != nil {
			return err
		}
	}
	return s.Start()
}

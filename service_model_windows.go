//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
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
	return &mgr.Service{Name: serviceName, Handle: sh}, nil
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

func serviceStartType(auto bool) uint32 {
	if auto {
		return windows.SERVICE_AUTO_START
	}
	return windows.SERVICE_DEMAND_START
}

type serviceConfigurer interface {
	Config() (mgr.Config, error)
	UpdateConfig(mgr.Config) error
}

func updateServiceStartup(s serviceConfigurer, auto bool) error {
	c, err := s.Config()
	if err != nil {
		return err
	}
	start := serviceStartType(auto)
	hasDeps := len(c.Dependencies) > 0
	if c.StartType == start && !c.DelayedAutoStart && hasDeps {
		return nil
	}
	c.StartType = start
	c.DelayedAutoStart = false
	if !hasDeps {
		c.Dependencies = []string{"Tcpip", "Dnscache"}
	}
	return s.UpdateConfig(c)
}

func setServiceAutoStart(auto bool) error {
	s, err := openSonDNSService(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return err
	}
	defer s.Close()
	return updateServiceStartup(s, auto)
}

func startInstalledService() error {
	s, err := openSonDNSService(windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Start()
}

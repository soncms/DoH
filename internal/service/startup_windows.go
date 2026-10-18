//go:build windows

package service

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

type serviceStartupController interface {
	serviceConfigurer
	SetStartupBase(start uint32, dependencies []string) error
	SetDelayedAutoStart(bool) error
}

type scmStartupController struct{ *mgr.Service }

// Unlike mgr.UpdateConfig, these calls never rewrite the executable, account,
// password, description, SID type or other unrelated service configuration.
func (s scmStartupController) SetStartupBase(start uint32, dependencies []string) error {
	block, err := serviceDependencyBlock(dependencies)
	if err != nil {
		return err
	}
	return windows.ChangeServiceConfig(s.Handle, windows.SERVICE_NO_CHANGE, start,
		windows.SERVICE_NO_CHANGE, nil, nil, nil, &block[0], nil, nil, nil)
}

func (s scmStartupController) SetDelayedAutoStart(enable bool) error {
	var flag uint32
	if enable {
		flag = 1
	}
	return windows.ChangeServiceConfig2(s.Handle, windows.SERVICE_CONFIG_DELAYED_AUTO_START_INFO,
		(*byte)(unsafe.Pointer(&flag)))
}

func serviceDependencyBlock(dependencies []string) ([]uint16, error) {
	if len(dependencies) == 0 {
		// An empty MULTI_SZ removes dependencies; a nil pointer leaves them alone.
		return []uint16{0, 0}, nil
	}
	var block []uint16
	for _, name := range dependencies {
		if name == "" {
			return nil, errors.New("invalid empty or NUL-containing service dependency")
		}
		encoded, err := windows.UTF16FromString(name)
		if err != nil {
			return nil, fmt.Errorf("invalid service dependency: %w", err)
		}
		block = append(block, encoded...)
	}
	return append(block, 0), nil
}

type serviceStartupChange struct {
	previous, applied mgr.Config
	baseChanged       bool
	delayedChanged    bool
}

func cloneServiceConfig(c mgr.Config) mgr.Config {
	c.Dependencies = slices.Clone(c.Dependencies)
	return c
}

func serviceStartupConfig(c mgr.Config, auto bool) mgr.Config {
	c = cloneServiceConfig(c)
	c.StartType = serviceStartType(auto)
	c.DelayedAutoStart = false
	if len(c.Dependencies) == 0 {
		c.Dependencies = []string{"Tcpip", "Dnscache"}
	}
	return c
}

func checkServiceStartupSnapshot(s serviceConfigurer, expected mgr.Config) error {
	current, err := s.Config()
	if err != nil {
		return fmt.Errorf("read service configuration before startup update: %w", err)
	}
	// SCM has no compare-and-swap API. Check before each write; narrow writes
	// also preserve unrelated settings changed after this final read.
	if !reflect.DeepEqual(current, expected) {
		return errors.New("service configuration changed concurrently; previous settings were not restored")
	}
	return nil
}

func applyServiceStartupChange(s serviceStartupController, auto bool) (*serviceStartupChange, error) {
	previous, err := s.Config()
	if err != nil {
		return nil, err
	}
	desired := serviceStartupConfig(previous, auto)
	change := &serviceStartupChange{previous: cloneServiceConfig(previous), applied: cloneServiceConfig(previous)}
	// Clear delayed startup before changing the base type. Reversing the order
	// on rollback restores automatic startup before re-enabling its delay.
	if previous.DelayedAutoStart != desired.DelayedAutoStart {
		if err := s.SetDelayedAutoStart(desired.DelayedAutoStart); err != nil {
			return nil, fmt.Errorf("change delayed service startup: %w", err)
		}
		change.delayedChanged = true
		change.applied.DelayedAutoStart = desired.DelayedAutoStart
	}
	if previous.StartType != desired.StartType || !slices.Equal(previous.Dependencies, desired.Dependencies) {
		err = checkServiceStartupSnapshot(s, change.applied)
		if err == nil {
			err = s.SetStartupBase(desired.StartType, desired.Dependencies)
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("change service startup: %w", err), change.rollback(s))
		}
		change.baseChanged = true
		change.applied.StartType = desired.StartType
		change.applied.Dependencies = slices.Clone(desired.Dependencies)
	}
	if !change.baseChanged && !change.delayedChanged {
		return nil, nil
	}
	return change, nil
}

func (c *serviceStartupChange) rollback(s serviceStartupController) error {
	if c.baseChanged {
		if err := checkServiceStartupSnapshot(s, c.applied); err != nil {
			return err
		}
		if err := s.SetStartupBase(c.previous.StartType, c.previous.Dependencies); err != nil {
			return fmt.Errorf("restore service startup type/dependencies: %w", err)
		}
		c.baseChanged = false
		c.applied.StartType = c.previous.StartType
		c.applied.Dependencies = slices.Clone(c.previous.Dependencies)
	}
	if c.delayedChanged {
		if err := checkServiceStartupSnapshot(s, c.applied); err != nil {
			return err
		}
		if err := s.SetDelayedAutoStart(c.previous.DelayedAutoStart); err != nil {
			return fmt.Errorf("restore delayed service startup: %w", err)
		}
		c.delayedChanged = false
		c.applied.DelayedAutoStart = c.previous.DelayedAutoStart
	}
	return nil
}

func updateServiceStartup(s serviceStartupController, auto bool) error {
	_, err := applyServiceStartupChange(s, auto)
	return err
}

//go:build windows && !console

package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type migrationTrayValue struct {
	Exists bool
	Kind   uint32
	Value  string
}

func readMigrationTray(root registry.Key, path string) (migrationTrayValue, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return migrationTrayValue{}, nil
	}
	if err != nil {
		return migrationTrayValue{}, err
	}
	defer k.Close()
	value, kind, err := k.GetStringValue(runRegistryName)
	if errors.Is(err, registry.ErrNotExist) {
		return migrationTrayValue{}, nil
	}
	return migrationTrayValue{Exists: err == nil, Kind: kind, Value: value}, err
}

func validateMigrationTray(value migrationTrayValue, source, target string) error {
	if !value.Exists {
		return nil
	}
	command := value.Value
	var err error
	if value.Kind == registry.EXPAND_SZ {
		command, err = registry.ExpandString(command)
		if err != nil {
			return err
		}
	} else if value.Kind != registry.SZ {
		return errors.New("unsupported SonDNS tray registry value type")
	}
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || (len(args) != 3 && len(args) != 5) || args[1] != "gui" || args[2] != "--tray" ||
		(!strings.EqualFold(filepath.Clean(args[0]), filepath.Clean(source)) && !strings.EqualFold(filepath.Clean(args[0]), filepath.Clean(target))) {
		return errors.New("SonDNS tray startup is unrelated to this migration; review it manually")
	}
	if len(args) == 5 {
		if args[3] != "--config" {
			return errors.New("unsupported tray startup arguments")
		}
		if _, err := PortableConfigPath(filepath.Dir(args[0]), args[4]); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot both content and type. Never overwrite a registry value changed
// since the confirmation dialog, or use another UAC account's HKCU.
func changeMigrationTrayAt(root registry.Key, path string, previous migrationTrayValue, exe, config string, enable bool) (func() error, error) {
	current, err := readMigrationTray(root, path)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, previous) {
		return nil, errors.New("tray startup changed; review migration again")
	}
	applied := migrationTrayValue{}
	if enable {
		command, err := guiTrayCommand(exe, config)
		if err != nil {
			return nil, err
		}
		applied = migrationTrayValue{true, registry.SZ, command}
	}
	if reflect.DeepEqual(previous, applied) {
		return nil, nil
	}
	if err := writeMigrationTray(root, path, applied); err != nil {
		return nil, err
	}
	return func() error {
		current, err := readMigrationTray(root, path)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(current, previous) {
			return nil
		}
		if !reflect.DeepEqual(current, applied) {
			return errors.New("tray startup changed concurrently; rollback kept the newer value")
		}
		return writeMigrationTray(root, path, previous)
	}, nil
}

func writeMigrationTray(root registry.Key, path string, value migrationTrayValue) error {
	k, _, err := registry.CreateKey(root, path, registry.QUERY_VALUE|registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	if !value.Exists {
		err = k.DeleteValue(runRegistryName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	if value.Kind == registry.EXPAND_SZ {
		return k.SetExpandStringValue(runRegistryName, value.Value)
	}
	if value.Kind != registry.SZ {
		return errors.New("unsupported SonDNS tray registry value type")
	}
	return k.SetStringValue(runRegistryName, value.Value)
}

//go:build windows

package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func defaultConfigForExe(exe string) string {
	return filepath.Join(filepath.Dir(exe), "data", "sondns.json")
}

// SCM and the GUI must interpret the same command identically. In particular,
// do not silently ignore a space-separated --config or accept duplicates.
func ServiceRunConfigPath(root string, args []string) (string, error) {
	if len(args) == 0 || args[0] != "run" {
		return "", errors.New("service command must start with run")
	}
	config := filepath.Join("data", "sondns.json")
	switch {
	case len(args) == 1:
	case len(args) == 2 && strings.HasPrefix(args[1], "--config="):
		config = strings.TrimPrefix(args[1], "--config=")
	case len(args) == 3 && args[1] == "--config":
		config = args[2]
	default:
		return "", errors.New("service command has unsupported or ambiguous arguments")
	}
	if config == "" {
		return "", errors.New("service config path is empty")
	}
	if _, err := windows.UTF16PtrFromString(config); err != nil {
		return "", err
	}
	return PortableConfigPath(root, config)
}

func serviceCommandConfigPath(command, exe string) (string, error) {
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || len(args) < 2 {
		return "", errors.New("service command is malformed")
	}
	if !filepath.IsAbs(args[0]) || !strings.EqualFold(filepath.Clean(args[0]), filepath.Clean(exe)) {
		return "", errors.New("service belongs to another SonDNS executable")
	}
	return ServiceRunConfigPath(filepath.Dir(exe), args[1:])
}

func requireServiceConfigForExe(s serviceConfigurer, exe, config string) error {
	c, err := s.Config()
	if err != nil {
		return err
	}
	return requireServiceCommandConfig(c.BinaryPathName, exe, config)
}

func requireServiceCommandConfig(command, exe, config string) error {
	actual, err := serviceCommandConfigPath(command, exe)
	if err != nil {
		return err
	}
	expected, err := PortableConfigPath(filepath.Dir(exe), config)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("service uses a different configuration: %s; reopen SonDNS without --config to manage it", actual)
	}
	return nil
}

func guiTrayCommand(exe, config string) (string, error) {
	if !filepath.IsAbs(exe) {
		return "", errors.New("tray executable must use an absolute path")
	}
	if config == "" {
		config = defaultConfigForExe(exe)
	}
	config, err := PortableConfigPath(filepath.Dir(exe), config)
	if err != nil {
		return "", err
	}
	for _, value := range []string{exe, config} {
		if _, err := windows.UTF16PtrFromString(value); err != nil {
			return "", err
		}
	}
	command := windows.ComposeCommandLine([]string{exe, "gui", "--tray", "--config", config})
	if len(windows.StringToUTF16(command))-1 > 260 {
		return "", errors.New("tray startup command exceeds the Windows Run 260-character limit; use a shorter portable folder/config path")
	}
	return command, nil
}

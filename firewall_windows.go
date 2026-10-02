//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const sondnsFirewallGroup = "SonDNS"

type FirewallManager struct{}

func (FirewallManager) ruleName(path string) string {
	return "SonDNS app " + filepath.Base(path)
}

func (f FirewallManager) BlockApp(path string) error {
	path, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || path == "" {
		return fmt.Errorf("invalid app path")
	}
	if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
		return fmt.Errorf("app executable not found: %s", path)
	}
	return runNetsh("advfirewall", "firewall", "add", "rule", "name="+f.ruleName(path),
		"group="+sondnsFirewallGroup, "dir=out", "action=block", "program="+path, "enable=yes", "profile=any")
}

func (f FirewallManager) AllowApp(path string) error {
	return runNetsh("advfirewall", "firewall", "delete", "rule", "name="+f.ruleName(path), "group="+sondnsFirewallGroup)
}

func (f FirewallManager) RemoveAll() error {
	err := runNetsh("advfirewall", "firewall", "delete", "rule", "group="+sondnsFirewallGroup)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "no rules") {
		return nil
	}
	return err
}

func runNetsh(args ...string) error {
	cmd := exec.Command("netsh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

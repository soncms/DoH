//go:build windows && !console

package main

import (
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
)

func TestGUIAutostartRegistryLifecycle(t *testing.T) {
	// Exercise real registry behavior under a temporary test key, never Run.
	path := `Software\SonDNS-test-` + rand.Text()
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	if err := syncGUIAutostartAt(registry.CURRENT_USER, path, "unused.exe", "", false); err != nil {
		t.Fatal(err)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ); err == nil {
		k.Close()
		t.Fatal("disabling autostart created a registry key")
	}
	exe := `F:\Portable Apps & Tools\SonDNS.exe`
	if err := syncGUIAutostartAt(registry.CURRENT_USER, path, exe, "", true); err != nil {
		t.Fatal(err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	command, _, err := k.GetStringValue(runRegistryName)
	if err != nil {
		t.Fatal(err)
	}
	args, err := windows.DecomposeCommandLine(command)
	if err != nil || !reflect.DeepEqual(args, []string{exe, "gui", "--tray", "--config", defaultConfigForExe(exe)}) {
		t.Fatalf("unsafe Run command quoting: %q %v", args, err)
	}
	if err := k.SetStringValue("unrelated", "preserved"); err != nil {
		t.Fatal(err)
	}
	if err := syncGUIAutostartAt(registry.CURRENT_USER, path, exe, "", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.GetStringValue(runRegistryName); !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("Run entry not removed: %v", err)
	}
	if value, _, err := k.GetStringValue("unrelated"); err != nil || value != "preserved" {
		t.Fatal("disabling SonDNS changed another registry value")
	}
}

func TestStartWithWindowsRollsBackOnlyCompletedChanges(t *testing.T) {
	for _, scenario := range []string{"query_failed", "service_failed", "tray_failed", "save_failed", "rollback_failed", "success", "not_installed"} {
		t.Run(scenario, func(t *testing.T) {
			previous := DefaultConfig()
			candidate := previous.Clone()
			candidate.ServiceAutoStart = true
			service := "disabled" // Different from the config's false/manual value.
			tray := "previous path"
			serviceCalls, trayCalls, saved := 0, 0, false
			result, err := applyStartWithWindows(previous, candidate, func() (serviceSnapshot, error) {
				if scenario == "query_failed" {
					return serviceSnapshot{}, errors.New("foreign installation")
				}
				return serviceSnapshot{Exists: scenario != "not_installed", State: svc.Stopped, StartType: windows.SERVICE_DISABLED}, nil
			}, func(bool) (func() error, error) {
				serviceCalls++
				if scenario == "service_failed" {
					return nil, errors.New("SCM failure")
				}
				service = "automatic"
				return func() error { service = "disabled"; return nil }, nil
			}, func(bool) (func() error, error) {
				trayCalls++
				if scenario == "tray_failed" {
					return nil, errors.New("registry failure")
				}
				tray = "new path"
				return func() error {
					if scenario == "rollback_failed" {
						return errors.New("tray rollback failure")
					}
					tray = "previous path"
					return nil
				}, nil
			}, func(Config) error {
				if scenario == "save_failed" || scenario == "rollback_failed" {
					return errors.New("disk failure")
				}
				saved = true
				return nil
			})
			success := scenario == "success" || scenario == "not_installed"
			if (err == nil) != success || result.ServiceAutoStart != success || saved != success {
				t.Fatalf("result=%v saved=%v err=%v", result.ServiceAutoStart, saved, err)
			}
			if scenario == "query_failed" && (serviceCalls != 0 || trayCalls != 0) {
				t.Fatal("foreign/query failure mutated startup registration")
			}
			if scenario == "service_failed" && trayCalls != 0 {
				t.Fatal("failed SCM update still changed tray registration")
			}
			if !success && service != "disabled" {
				t.Fatal("failed transaction did not restore actual prior SCM state")
			}
			if !success && scenario != "rollback_failed" && tray != "previous path" {
				t.Fatal("failed transaction did not restore actual prior Run entry")
			}
			if scenario == "rollback_failed" && !strings.Contains(err.Error(), "rollback failure") {
				t.Fatal("rollback failure was hidden")
			}
			if scenario == "not_installed" && serviceCalls != 0 {
				t.Fatal("saving future startup settings tried to modify an absent service")
			}
		})
	}
}

func TestGUIAutostartUndoPreservesRegistryType(t *testing.T) {
	path := `Software\SonDNS-test-` + rand.Text()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, path)
	defer k.Close()
	previous := `F:\SonDNS\SonDNS.exe gui --tray --legacy-option`
	if err := k.SetExpandStringValue(runRegistryName, previous); err != nil {
		t.Fatal(err)
	}
	undo, err := changeGUIAutostartAt(registry.CURRENT_USER, path, `F:\SonDNS\SonDNS.exe`, "", true)
	if err != nil || undo == nil {
		t.Fatalf("change registration: %v", err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	value, kind, err := k.GetStringValue(runRegistryName)
	if err != nil || value != previous || kind != registry.EXPAND_SZ {
		t.Fatalf("previous Run entry was not restored exactly: %q type=%d err=%v", value, kind, err)
	}
}

func TestGUIAutostartRefusesForeignCopyAndConcurrentRollback(t *testing.T) {
	path := `Software\SonDNS-test-` + rand.Text()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.DeleteKey(registry.CURRENT_USER, path)
	defer k.Close()
	exe := `F:\SonDNS\SonDNS.exe`
	foreign := `F:\AnotherCopy\SonDNS.exe gui --tray`
	if err := k.SetStringValue(runRegistryName, foreign); err != nil {
		t.Fatal(err)
	}
	for _, enable := range []bool{false, true} {
		if _, err := changeGUIAutostartAt(registry.CURRENT_USER, path, exe, "", enable); err == nil {
			t.Fatal("another installation's Run entry was changed")
		}
	}
	if value, _, _ := k.GetStringValue(runRegistryName); value != foreign {
		t.Fatal("foreign Run entry was overwritten")
	}
	if err := k.DeleteValue(runRegistryName); err != nil {
		t.Fatal(err)
	}
	undo, err := changeGUIAutostartAt(registry.CURRENT_USER, path, exe, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.SetStringValue(runRegistryName, foreign); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err == nil {
		t.Fatal("rollback overwrote a subsequent registry change")
	}
	if value, _, _ := k.GetStringValue(runRegistryName); value != foreign {
		t.Fatal("concurrent Run update was overwritten")
	}
}

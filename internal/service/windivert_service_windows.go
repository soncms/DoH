//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const winDivertServiceName = "WinDivert"

// cleanupWinDivertDriver removes only the WinDivert driver service created
// from this executable's own driver file. YogaDNS uses a different driver and
// is intentionally left untouched.
func cleanupWinDivertDriver() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(winDivertServiceName)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return err
	}
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	servicePath := strings.Trim(strings.TrimSpace(c.BinaryPathName), `"`)
	servicePath = strings.TrimPrefix(servicePath, `\??\`)
	if !isOurWinDivertPath(servicePath, exe) {
		return fmt.Errorf("refusing to remove WinDivert service at %q", c.BinaryPathName)
	}
	st, err := s.Query()
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return err
	}
	if st.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return err
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			st, err = s.Query()
			if err != nil {
				if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
					return nil
				}
				return err
			}
			if st.State == svc.Stopped {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if st.State != svc.Stopped {
			return errors.New("WinDivert driver has not stopped; leaving its service registered")
		}
	}
	if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return err
	}
	return nil
}

func isOurWinDivertPath(servicePath, exe string) bool {
	clean := filepath.Clean(servicePath)
	if insidePortableRuntime(clean) {
		return true
	}
	if exe != "" {
		if strings.EqualFold(clean, filepath.Clean(filepath.Join(filepath.Dir(exe), "WinDivert64.sys"))) {
			return true
		}
	}
	return false
}

//go:build windows && !console

package service

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type lifecycleDPIProcess struct {
	handle windows.Handle
	pid    uint32
}

// Open and verify the process image on the same handle later used for waits or
// termination. Never identify a process for termination by name or PID alone.
func findLifecycleDPI(path string) (*lifecycleDPIProcess, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(path)) {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue // Exited between the snapshot and OpenProcess.
		}
		if err != nil {
			return nil, err
		}
		image := make([]uint16, 32768)
		size := uint32(len(image))
		err = windows.QueryFullProcessImageName(h, 0, &image[0], &size)
		if err != nil {
			_ = windows.CloseHandle(h)
			return nil, err
		}
		if !strings.EqualFold(filepath.Clean(windows.UTF16ToString(image[:size])), filepath.Clean(path)) {
			_ = windows.CloseHandle(h)
			continue
		}
		return &lifecycleDPIProcess{handle: h, pid: entry.ProcessID}, nil
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return nil, nil
}

func waitForLifecycleDPI(t *testing.T, path string, previousPID uint32) *lifecycleDPIProcess {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); ; {
		p, err := findLifecycleDPI(path)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			t.Cleanup(func() { _ = windows.CloseHandle(p.handle) })
			if p.pid == previousPID {
				t.Fatal("supervisor did not replace the terminated DPI process")
			}
			status, err := windows.WaitForSingleObject(p.handle, 500)
			if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
				t.Fatalf("embedded DPI helper exited during startup: wait=%d err=%v", status, err)
			}
			if err := verifyRuntimeAssetFile("third_party/zapret2/winws2.exe", path); err != nil {
				t.Fatal(err)
			}
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("embedded DPI helper did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func requireLifecycleDPIExited(t *testing.T, p *lifecycleDPIProcess) {
	t.Helper()
	status, err := windows.WaitForSingleObject(p.handle, 3000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("DPI helper survived service stop: pid=%d wait=%d err=%v", p.pid, status, err)
	}
}

func requireNoLifecycleDPI(t *testing.T, path string) {
	t.Helper()
	// Include the supervisor's maximum restart delay: a one-time absence can miss
	// a helper that incorrectly restarts after its controlling service stops.
	for deadline := time.Now().Add(4500 * time.Millisecond); ; {
		p, err := findLifecycleDPI(path)
		if err != nil {
			t.Fatal(err)
		}
		if p != nil {
			_ = windows.CloseHandle(p.handle)
			t.Fatalf("orphan or restarted DPI helper after service stop: pid=%d", p.pid)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

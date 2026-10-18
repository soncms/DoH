//go:build windows

package service

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

type processMutex struct{ handle windows.Handle }

func AcquireProcessMutex(name string) (*processMutex, bool, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateMutex(nil, false, n)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, false, fmt.Errorf("create instance mutex: %w", err)
	}
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return nil, false, nil
	}
	return &processMutex{handle: h}, true, nil
}

func (m *processMutex) Close() {
	if m != nil && m.handle != 0 {
		_ = windows.CloseHandle(m.handle)
		m.handle = 0
	}
}

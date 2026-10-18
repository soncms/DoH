//go:build windows

package service

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

const winDivertShutdownBoth = 3

func (i *Interceptor) Shutdown() {
	if i == nil {
		return
	}
	i.stopOnce.Do(func() {
		if i.cancel != nil {
			i.cancel()
		}
		valid := i.handle != 0 && i.handle != windows.InvalidHandle
		if valid && i.shutdown != nil {
			// WinDivert 2.x stops capture and completes an empty pending receive
			// with ERROR_NO_DATA. No local or public UDP wake packet is needed.
			ok, _, err := i.shutdown.Call(uintptr(i.handle), winDivertShutdownBoth)
			if ok == 0 {
				if i.log != nil {
					i.log.Printf("WinDivert shutdown failed: %v", err)
				}
				if err := windows.CancelIoEx(i.handle, nil); err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) && i.log != nil {
					i.log.Printf("cancel WinDivert I/O: %v", err)
				}
			}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			i.wg.Wait()
			// An in-flight receive/send must finish before Windows can recycle
			// this handle value for an unrelated object. A slow worker retains
			// the shut-down handle until it exits, even after the bounded wait.
			if valid && i.close != nil {
				if ok, _, err := i.close.Call(uintptr(i.handle)); ok == 0 && i.log != nil {
					i.log.Printf("close WinDivert handle: %v", err)
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			if i.log != nil {
				i.log.Printf("WinDivert shutdown wait timed out; handle will close after all packet workers finish")
			}
		}
	})
}

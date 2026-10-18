//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func privateIPCServer(t *testing.T) *dnsIPCServer {
	t.Helper()
	s := newDNSIPCServer(NewDNSRequestRing(), nil)
	s.pipeName = fmt.Sprintf(`\\.\pipe\SonDNS-regression-%d-%d`, os.Getpid(), time.Now().UnixNano())
	if err := s.start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func openPartialIPC(t *testing.T, s *dnsIPCServer) *os.File {
	t.Helper()
	name, _ := windows.UTF16PtrFromString(s.pipeName)
	var h windows.Handle
	var err error
	for deadline := time.Now().Add(time.Second); ; {
		h, err = windows.CreateFile(name, ipcClientAccess, 0, nil, windows.OPEN_EXISTING, ipcClientFlags, 0)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(h), "partial IPC client")
	t.Cleanup(func() { _ = f.Close() })
	if err = f.SetDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIPCPartialClientDoesNotBlockOthers(t *testing.T) {
	s := privateIPCServer(t)
	_ = openPartialIPC(t, s)
	if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatalf("partial request blocked IPC: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestIPCReadDeadlineReleasesAllSlots(t *testing.T) {
	s := privateIPCServer(t)
	clients := make([]*os.File, ipcMaxHandlers)
	for i := range clients {
		clients[i] = openPartialIPC(t, s)
	}
	started := time.Now()
	for _, f := range clients {
		var one [1]byte
		if _, err := f.Read(one[:]); err == nil {
			t.Fatal("incomplete request accepted")
		}
	}
	if time.Since(started) > ipcIOTimeout+time.Second {
		t.Fatal("server failed to expire stalled clients")
	}
	if _, err := callDNSIPCAt(s.pipeName, dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
}

func TestIPCOversizedRequestAndShutdown(t *testing.T) {
	s := privateIPCServer(t)
	f := openPartialIPC(t, s)
	_, _ = f.Write([]byte(strings.Repeat(" ", ipcMaxRequestBytes) + "\n"))
	var one [1]byte
	if _, err := f.Read(one[:]); err == nil {
		t.Fatal("oversized request accepted")
	}
	_ = openPartialIPC(t, s)
	started := time.Now()
	s.close()
	if time.Since(started) > time.Second {
		t.Fatal("shutdown did not interrupt pipe I/O")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.active) != 0 {
		t.Fatalf("shutdown retained %d handles", len(s.active))
	}
}

func TestIPCShutdownPendingAcceptRepeated(t *testing.T) {
	for i := 0; i < 32; i++ {
		var s *dnsIPCServer
		if i%2 == 0 {
			// Also cover shutdown before the first client ever connects.
			s = newDNSIPCServer(NewDNSRequestRing(), nil)
			s.pipeName = fmt.Sprintf(`\\.\pipe\SonDNS-unconnected-%d-%d`, os.Getpid(), time.Now().UnixNano())
			if err := s.start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.close)
		} else {
			s = privateIPCServer(t)
		}
		deadline := time.Now().Add(time.Second)
		for {
			s.mu.Lock()
			pending := false
			for _, f := range s.active {
				if f == nil {
					pending = true
				}
			}
			s.mu.Unlock()
			if pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("listener did not post its next accept")
			}
			time.Sleep(time.Millisecond)
		}
		done := make(chan struct{})
		go func() { s.close(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("shutdown hung cancelling a pending accept")
		}
	}
}

func TestIPCWriteDeadlineReleasesSlowReader(t *testing.T) {
	s := privateIPCServer(t)
	for i := 0; i < maxDNSLogRows; i++ {
		s.ring.Add(DNSRequestEntry{Domain: strings.Repeat("x", 4096), Type: "A", Result: "NOERROR"})
	}
	f := openPartialIPC(t, s)
	// Complete the opening brace from openPartialIPC, then never read the reply.
	if _, err := f.Write([]byte("\"op\":\"snapshot\"}\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(ipcIOTimeout + time.Second)
	time.Sleep(30 * time.Millisecond)
	for {
		s.mu.Lock()
		handlers := 0
		for _, file := range s.active {
			if file != nil {
				handlers++
			}
		}
		s.mu.Unlock()
		if handlers == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("slow reader held a handler past its write deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

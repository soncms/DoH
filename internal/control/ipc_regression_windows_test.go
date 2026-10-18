//go:build windows

package control

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"sondns/internal/requestlog"
	"sondns/internal/testutil"
)

func privateIPCServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(requestlog.New(), nil)
	s.PipeName = fmt.Sprintf(`\\.\pipe\SonDNS-regression-%d-%d`, os.Getpid(), time.Now().UnixNano())
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, err := CallAt(s.PipeName, Request{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIPCPartialClientDoesNotBlockOthers(t *testing.T) {
	s := privateIPCServer(t)
	_ = testutil.OpenPartialIPC(t, s.PipeName)
	if _, err := CallAt(s.PipeName, Request{Op: "snapshot"}); err != nil {
		t.Fatalf("partial request blocked IPC: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := CallAt(s.PipeName, Request{Op: "snapshot"}); err != nil {
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
		clients[i] = testutil.OpenPartialIPC(t, s.PipeName)
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
	if _, err := CallAt(s.PipeName, Request{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
}

func TestIPCOversizedRequestAndShutdown(t *testing.T) {
	s := privateIPCServer(t)
	f := testutil.OpenPartialIPC(t, s.PipeName)
	_, _ = f.Write([]byte(strings.Repeat(" ", ipcMaxRequestBytes) + "\n"))
	var one [1]byte
	if _, err := f.Read(one[:]); err == nil {
		t.Fatal("oversized request accepted")
	}
	_ = testutil.OpenPartialIPC(t, s.PipeName)
	started := time.Now()
	s.Close()
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
		var s *Server
		if i%2 == 0 {
			// Also cover shutdown before the first client ever connects.
			s = NewServer(requestlog.New(), nil)
			s.PipeName = fmt.Sprintf(`\\.\pipe\SonDNS-unconnected-%d-%d`, os.Getpid(), time.Now().UnixNano())
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
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
		go func() { s.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("shutdown hung cancelling a pending accept")
		}
	}
}

func TestIPCWriteDeadlineReleasesSlowReader(t *testing.T) {
	s := privateIPCServer(t)
	for i := 0; i < requestlog.Capacity; i++ {
		s.ring.Add(requestlog.Entry{Domain: strings.Repeat("x", 4096), Type: "A", Result: "NOERROR"})
	}
	f := testutil.OpenPartialIPC(t, s.PipeName)
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

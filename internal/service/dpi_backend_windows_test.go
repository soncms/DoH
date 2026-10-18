//go:build windows

package service

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "sondns/internal/config"
)

func TestZapret2CaptureModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want []string
		miss []string
	}{
		{mode: "default", want: []string{"--wf-tcp-out=80,443", "--wf-filter-lan=1", "--wf-filter-loopback=1"}, miss: []string{"--wf-udp-out=443"}},
		{mode: "tcp", want: []string{"--wf-tcp-out=1-52,54-65535", "--filter-tcp=1-65535"}, miss: []string{"--wf-udp-out=443"}},
		{mode: "tcp+udp", want: []string{"--wf-tcp-out=1-52,54-65535", "--wf-udp-out=443"}, miss: nil},
	} {
		args, err := zapret2Arguments(t.TempDir(), appconfig.DPIConfig{CaptureMode: tc.mode})
		if err != nil {
			t.Fatalf("mode %q: %v", tc.mode, err)
		}
		joined := strings.Join(args, "\x00")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("mode %q missing %q", tc.mode, want)
			}
		}
		for _, miss := range tc.miss {
			if strings.Contains(joined, miss) {
				t.Errorf("mode %q unexpectedly contains %q", tc.mode, miss)
			}
		}
	}
}

// Hold the launch log so Stop deterministically arrives before Cmd.Start.
// This exercises the real process path without starting a DPI engine/driver.
func TestDPIBackendStopDuringLaunch(t *testing.T) {
	if ready := os.Getenv("SONDNS_DPI_LAUNCH_FIXTURE"); ready != "" {
		if err := os.WriteFile(ready, []byte("started"), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	ready := filepath.Join(t.TempDir(), "helper-started")
	w := &dpiLaunchBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	b := &DPIBackend{
		logger: log.New(w, "", 0),
		env:    append(os.Environ(), "SONDNS_DPI_LAUNCH_FIXTURE="+ready),
		stop:   make(chan struct{}), done: make(chan struct{}),
	}
	t.Cleanup(func() {
		w.unblock()
		b.Shutdown()
		b.mu.Lock()
		cmd := b.cmd
		b.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			t.Error("fixture supervisor survived cleanup")
		}
	})
	go b.run(os.Args[0], []string{"-test.run=^TestDPIBackendStopDuringLaunch$"})
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not reach launch barrier")
	}
	stopped := make(chan struct{})
	go func() { b.Shutdown(); close(stopped) }()
	select {
	case <-b.stop:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not signal stop")
	}
	w.unblock()
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop missed the helper launch and left its supervisor running")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not finish")
	}
	if _, err := os.Stat(ready); !os.IsNotExist(err) {
		t.Fatalf("helper launched after Stop: %v", err)
	}
}

type dpiLaunchBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	open    sync.Once
}

func (w *dpiLaunchBarrier) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "starting DPI backend") {
		w.once.Do(func() { close(w.entered) })
		<-w.release
	}
	return len(p), nil
}

func (w *dpiLaunchBarrier) unblock() { w.open.Do(func() { close(w.release) }) }

//go:build windows

package main

import (
	"context"
	"os"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/lxn/walk"
	"github.com/miekg/dns"
)

// Explicit opt-in: exercises the real GUI poller against the running service.
// Sends 20 example.com queries without clearing logs/cache or changing settings.
func TestNativeLiveLogLatency(t *testing.T) {
	if os.Getenv("SONDNS_UI_TEST") != "1" || os.Getenv("SONDNS_LOAD_TEST") != "1" {
		t.Skip("set SONDNS_UI_TEST=1 and SONDNS_LOAD_TEST=1 to measure live log latency")
	}
	c, err := LoadConfig(defaultConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = callDNSIPC(dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mw, err := walk.NewMainWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer mw.Dispose()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := &guiState{mw: mw, ctx: ctx, cancel: cancel, config: c}
	if err = g.build(); err != nil {
		t.Fatal(err)
	}
	mw.SetPersistent(false)
	mw.Show()
	done := make(chan struct{})
	mw.Starting().Attach(func() {
		go g.monitorRequestLog()
		go func() {
			defer close(done)
			defer g.onUI(func() { cancel(); _ = mw.Close() })
			client := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
			samples := make([]time.Duration, 0, 20)
			for i := 0; i < 20; i++ {
				// Avoid phase-locking the queries to the GUI's poll interval.
				time.Sleep(time.Duration(31+i*13) * time.Millisecond)
				started := time.Now()
				msg := new(dns.Msg)
				msg.SetQuestion("example.com.", dns.TypeA)
				reply, _, err := client.Exchange(msg, c.Listen)
				responded := time.Now()
				if err != nil || reply.Rcode != dns.RcodeSuccess {
					t.Errorf("live DNS query failed: %v", err)
					return
				}
				seen := make(chan time.Time, 1)
				for time.Since(responded) < 2*time.Second {
					g.onUI(func() {
						for _, row := range g.logModel.rows {
							if row.Domain == "example.com" && !row.Time.Before(started) {
								select {
								case seen <- time.Now():
								default:
								}
								return
							}
						}
					})
					select {
					case at := <-seen:
						samples = append(samples, at.Sub(responded))
					case <-time.After(10 * time.Millisecond):
						continue
					}
					break
				}
				if len(samples) != i+1 {
					response, ipcErr := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
					t.Logf("service snapshot version=%d rows=%d err=%v", response.Version, len(response.Entries), ipcErr)
					g.onUI(func() {
						t.Logf("GUI version=%d rows=%d pending=%v lastError=%q", g.logVersion, len(g.logModel.rows), g.logPollPending, g.lastLogError)
					})
					t.Error("completed DNS query did not appear in the GUI within two seconds")
					return
				}
			}
			slices.Sort(samples)
			t.Logf("reply-to-GUI samples=%d p50=%s p95=%s max=%s", len(samples), samples[len(samples)/2], samples[len(samples)*95/100], samples[len(samples)-1])
		}()
	})
	mw.Run()
	<-done
}

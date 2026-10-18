//go:build windows && console

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// Opt-in checks affect only the explicitly named loopback preview, never SCM.
func TestConsoleLivePreview(t *testing.T) {
	root := os.Getenv("SONDNS_CONSOLE_PREVIEW_ROOT")
	if root == "" {
		t.Skip("set SONDNS_CONSOLE_PREVIEW_ROOT to a running preview folder")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("preview root must be absolute")
	}
	pipe := consolePipe(root, true)
	if _, err := callDNSIPCAt(pipe, dnsIPCRequest{Op: "state"}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(filepath.Join(root, "data", "sondns.json"))
	if err != nil {
		t.Fatal(err)
	}
	address := "127.0.0.1:15300"
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			results := make(chan time.Duration, 500)
			failures := make(chan error, 500)
			var wg sync.WaitGroup
			for w := 0; w < 10; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 50; i++ {
						q := new(dns.Msg)
						q.SetQuestion("example.com.", dns.TypeA)
						r, elapsed, err := (&dns.Client{Net: network, Timeout: 5 * time.Second}).Exchange(q, address)
						if err == nil && (r.Id != q.Id || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0) {
							err = fmt.Errorf("invalid reply: %v", r)
						}
						if err != nil {
							failures <- err
						} else {
							results <- elapsed
						}
					}
				}()
			}
			wg.Wait()
			close(results)
			close(failures)
			for err := range failures {
				t.Error(err)
			}
			var timings []time.Duration
			for elapsed := range results {
				timings = append(timings, elapsed)
			}
			slices.Sort(timings)
			if len(timings) != 500 {
				t.Fatalf("successful=%d/500", len(timings))
			}
			t.Logf("queries=500 errors=0 p50=%s p95=%s", timings[250], timings[475])
		})
	}
	snapshot, err := callDNSIPCAt(pipe, dnsIPCRequest{Op: "snapshot"})
	if err != nil || len(snapshot.Entries) != maxDNSLogRows {
		t.Fatalf("RAM log: rows=%d err=%v", len(snapshot.Entries), err)
	}
	for _, row := range snapshot.Entries {
		if row.AttemptCount < 1 || row.AttemptCount > 2 {
			t.Fatalf("attempt count=%d", row.AttemptCount)
		}
	}
	_ = openPartialIPC(t, &dnsIPCServer{pipeName: pipe})
	if _, err = callDNSIPCAt(pipe, dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatal("partial client blocked pipe:", err)
	}
	if _, err = callDNSIPCAt(pipe, dnsIPCRequest{Op: "set_upstream", Upstreams: []Upstream{{URL: "udp://127.0.0.1:53"}}}); err == nil {
		t.Fatal("accepted plaintext")
	}
	for _, enabled := range []bool{true, false} {
		items := c.Clone().Upstreams
		for i := range items {
			items[i].DNSSEC = enabled
		}
		if _, err = callDNSIPCAt(pipe, dnsIPCRequest{Op: "set_upstream", Upstreams: items}); err != nil {
			t.Fatal(err)
		}
		if err = probeConsoleDNS(t.Context(), address); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = callDNSIPCAt(pipe, dnsIPCRequest{Op: "clear_log"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = callDNSIPCAt(pipe, dnsIPCRequest{Op: "snapshot"})
	if err != nil || len(snapshot.Entries) != 0 {
		t.Fatalf("clear RAM log failed: %v", err)
	}
	t.Log("IPC partial/validation, DNSSEC live swaps, 200-row bound and clear: PASS")
}

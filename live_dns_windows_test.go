//go:build windows

package main

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func liveDNSConfigPath() string {
	if path := os.Getenv("SONDNS_LIVE_CONFIG"); path != "" {
		return path
	}
	return defaultConfigPath()
}
func liveDNSPipeName() string {
	if pipe := os.Getenv("SONDNS_LIVE_PIPE"); pipe != "" {
		return pipe
	}
	return sondnsPipeName
}

// Explicit opt-in: queries the configured live resolver without changing its
// cache, upstreams, log, or Windows service configuration.
func TestLiveDNSLoad(t *testing.T) {
	if os.Getenv("SONDNS_LOAD_TEST") != "1" {
		t.Skip("set SONDNS_LOAD_TEST=1 to query the running service")
	}
	c, err := LoadConfig(liveDNSConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			const workers, perWorker = 10, 50
			latencies := make(chan time.Duration, workers*perWorker)
			errors := make(chan error, workers*perWorker)
			var wg sync.WaitGroup
			started := time.Now()
			for worker := 0; worker < workers; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					client := &dns.Client{Net: network, Timeout: 5 * time.Second}
					for i := 0; i < perWorker; i++ {
						msg := new(dns.Msg)
						msg.SetQuestion("example.com.", dns.TypeA)
						reply, elapsed, err := client.Exchange(msg, c.Listen)
						if err != nil {
							errors <- err
							continue
						}
						if reply.Id != msg.Id || reply.Rcode != dns.RcodeSuccess || len(reply.Answer) == 0 {
							errors <- fmt.Errorf("unexpected response: id=%d code=%d answers=%d", reply.Id, reply.Rcode, len(reply.Answer))
							continue
						}
						latencies <- elapsed
					}
				}()
			}
			wg.Wait()
			wall := time.Since(started)
			close(latencies)
			close(errors)
			for err := range errors {
				t.Error(err)
			}
			samples := make([]time.Duration, 0, workers*perWorker)
			for elapsed := range latencies {
				samples = append(samples, elapsed)
			}
			slices.Sort(samples)
			if len(samples) == 0 {
				t.Fatal("no successful responses")
			}
			t.Logf("queries=%d wall=%s p50=%s p95=%s max=%s", len(samples), wall, samples[len(samples)/2], samples[len(samples)*95/100], samples[len(samples)-1])
		})
	}
	response, err := callDNSIPCAt(liveDNSPipeName(), dnsIPCRequest{Op: "snapshot"})
	if err != nil || len(response.Entries) != maxDNSLogRows {
		t.Fatalf("live log not bounded after load: rows=%d err=%v", len(response.Entries), err)
	}
}

// TEST-NET-3 has no public DNS resolver. A valid reply from this destination
// exercises transparent interception rather than the loopback listener.
func TestLiveWinDivertDNS(t *testing.T) {
	if os.Getenv("SONDNS_LOAD_TEST") != "1" {
		t.Skip("set SONDNS_LOAD_TEST=1 to exercise live interception")
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	client := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
	r, elapsed, err := client.Exchange(q, "203.0.113.53:53")
	if err != nil {
		t.Fatal(err)
	}
	if r.Id != q.Id || r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		t.Fatalf("intercepted response invalid: %v", r)
	}
	t.Logf("WinDivert answered UDP/53 to TEST-NET-3 in %s", elapsed)
}

func TestLiveIPCPartialClientAndValidation(t *testing.T) {
	if os.Getenv("SONDNS_LOAD_TEST") != "1" {
		t.Skip("set SONDNS_LOAD_TEST=1 to exercise live IPC")
	}
	_ = openPartialIPC(t, &dnsIPCServer{pipeName: liveDNSPipeName()})
	if _, err := callDNSIPCAt(liveDNSPipeName(), dnsIPCRequest{Op: "snapshot"}); err != nil {
		t.Fatalf("partial client blocked LocalSystem service IPC: %v", err)
	}
	response, err := callDNSIPCAt(liveDNSPipeName(), dnsIPCRequest{Op: "set_upstream", Upstreams: []Upstream{{URL: "udp://127.0.0.1:53"}}})
	if err == nil || response.OK {
		t.Fatal("live service accepted unencrypted upstream")
	}
	t.Logf("LocalSystem concurrent IPC works; plain DNS rejected: %s", response.Error)
}

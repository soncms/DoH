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

// Explicit opt-in: queries the configured live resolver without changing its
// cache, upstreams, log, or Windows service configuration.
func TestLiveDNSLoad(t *testing.T) {
	if os.Getenv("SONDNS_LOAD_TEST") != "1" {
		t.Skip("set SONDNS_LOAD_TEST=1 to query the running service")
	}
	c, err := LoadConfig(defaultConfigPath())
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
	response, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
	if err != nil || len(response.Entries) != maxDNSLogRows {
		t.Fatalf("live log not bounded after load: rows=%d err=%v", len(response.Entries), err)
	}
}

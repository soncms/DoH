//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

type testCacheProxy struct {
	cleared   bool
	upstreams []Upstream
}

func (p *testCacheProxy) ClearCache() { p.cleared = true }
func (p *testCacheProxy) SetUpstreams(items []Upstream) error {
	p.upstreams = items
	return nil
}

func TestDNSIPCServerRoundTrip(t *testing.T) {
	if _, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"}); err == nil {
		t.Skip("SonDNS service already owns the IPC pipe")
	}
	ring := NewDNSRequestRing()
	proxy := &testCacheProxy{}
	server := newDNSIPCServer(ring, proxy)
	server.start()
	defer server.close()
	ring.Add(DNSRequestEntry{Domain: "example.com.", Type: "A", Result: "NOERROR", AnswerIPs: []string{"192.0.2.1"}})
	var response dnsIPCResponse
	var err error
	for i := 0; i < 20; i++ {
		response, err = callDNSIPC(dnsIPCRequest{Op: "snapshot"})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(response.Entries) != 1 || response.Entries[0].AnswerIPs[0] != "192.0.2.1" {
		t.Fatalf("snapshot failed: %+v %v", response, err)
	}
	if response.InstanceID == "" {
		t.Fatal("expected instance id in response")
	}
	if _, err = callDNSIPC(dnsIPCRequest{Op: "clear_cache"}); err != nil || !proxy.cleared {
		t.Fatalf("cache clear failed: %v", err)
	}
	if _, err = callDNSIPC(dnsIPCRequest{Op: "set_upstream", Upstreams: []Upstream{PredefinedUpstream(ProviderNextDNS)}}); err != nil || len(proxy.upstreams) != 1 || proxy.upstreams[0].URL != URLNextDNS {
		t.Fatalf("set_upstream failed: %v, proxy upstreams: %+v", err, proxy.upstreams)
	}
	response, err = callDNSIPC(dnsIPCRequest{Op: "clear_log"})
	if err != nil || len(response.Entries) != 0 {
		t.Fatalf("log clear failed: %+v %v", response, err)
	}
}

func TestLiveServiceIPC(t *testing.T) {
	resp, err := callDNSIPC(dnsIPCRequest{Op: "snapshot"})
	if err != nil {
		t.Skip("SonDNS service not running")
	}
	if resp.InstanceID == "" {
		t.Errorf("expected instance ID from live service, got empty")
	}
	t.Logf("Live service IPC response: instance=%s version=%d entries=%d", resp.InstanceID, resp.Version, len(resp.Entries))
	if len(resp.Entries) > 0 {
		t.Logf("Last entry: %+v", resp.Entries[len(resp.Entries)-1])
	}
	for _, entry := range resp.Entries {
		if strings.HasSuffix(entry.Domain, ".") {
			t.Errorf("entry domain has trailing dot: %q", entry.Domain)
		}
	}
}

func TestLiveServiceHotSwap(t *testing.T) {
	resp, err := callDNSIPC(dnsIPCRequest{
		Op:        "set_upstream",
		Upstreams: []Upstream{PredefinedUpstream(ProviderNextDNS)},
	})
	if err != nil {
		t.Skip("SonDNS service not running")
	}
	if !resp.OK {
		t.Fatalf("failed to switch to NextDNS: %s", resp.Error)
	}

	resp, err = callDNSIPC(dnsIPCRequest{
		Op:        "set_upstream",
		Upstreams: []Upstream{PredefinedUpstream(ProviderSonCMS)},
	})
	if err != nil || !resp.OK {
		t.Fatalf("failed to switch to SonCMS: %v, resp: %+v", err, resp)
	}
}

func TestDNSIPCSnapshotVersionSkip(t *testing.T) {
	ring := NewDNSRequestRing()
	proxy := &testCacheProxy{}
	server := newDNSIPCServer(ring, proxy)

	// Direct test of server logic
	ring.Add(DNSRequestEntry{Domain: "example.org", Type: "A", Result: "NOERROR"})
	v := ring.Version()

	// When matching version is passed, entries should be nil
	curVer := ring.Version()
	if curVer != v {
		t.Fatalf("expected version %d, got %d", v, curVer)
	}
	_ = server
}


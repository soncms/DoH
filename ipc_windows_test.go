//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type testEngineProxy struct {
	upstreams []Upstream
}

func (p *testEngineProxy) SetUpstreams(items []Upstream) error {
	p.upstreams = items
	return nil
}

func TestDNSIPCServerRoundTrip(t *testing.T) {
	ring := NewDNSRequestRing()
	proxy := &testEngineProxy{}
	server := newDNSIPCServer(ring, proxy)
	server.pipeName = fmt.Sprintf(`\\.\pipe\SonDNS-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	call := func(req dnsIPCRequest) (dnsIPCResponse, error) { return callDNSIPCAt(server.pipeName, req) }
	server.start()
	defer server.close()
	ring.Add(DNSRequestEntry{Domain: "example.com.", Type: "A", Result: "NOERROR", AnswerIPs: []string{"192.0.2.1"}})
	var response dnsIPCResponse
	var err error
	for i := 0; i < 20; i++ {
		response, err = call(dnsIPCRequest{Op: "snapshot"})
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
	if _, err = call(dnsIPCRequest{Op: "set_upstream", Upstreams: []Upstream{PredefinedUpstream(ProviderNextDNS)}}); err != nil || len(proxy.upstreams) != 1 || proxy.upstreams[0].URL != URLNextDNS {
		t.Fatalf("set_upstream failed: %v, proxy upstreams: %+v", err, proxy.upstreams)
	}
	response, err = call(dnsIPCRequest{Op: "clear_log"})
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
	if os.Getenv("SONDNS_HOTSWAP_TEST") != "1" {
		t.Skip("set SONDNS_HOTSWAP_TEST=1 to explicitly change the live service provider")
	}
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
	proxy := &testEngineProxy{}
	server := newDNSIPCServer(ring, proxy)

	ring.Add(DNSRequestEntry{Domain: "example.org", Type: "A", Result: "NOERROR"})
	v := ring.Version()
	resp := server.respond(dnsIPCRequest{Op: "snapshot", Version: v, InstanceID: server.instanceID})
	if !resp.Unchanged || len(resp.Entries) != 0 || resp.Version != v {
		t.Fatalf("unchanged snapshot: %+v", resp)
	}
	resp = server.respond(dnsIPCRequest{Op: "snapshot", Version: v, InstanceID: "previous-service"})
	if resp.Unchanged || len(resp.Entries) != 1 {
		t.Fatalf("version collision after restart suppressed rows: %+v", resp)
	}
	ring.Clear()
	resp = server.respond(dnsIPCRequest{Op: "snapshot", Version: v, InstanceID: server.instanceID})
	if resp.Unchanged || len(resp.Entries) != 0 || resp.Version <= v {
		t.Fatalf("clear must return a changed, empty snapshot: %+v", resp)
	}
}

func TestDNSIPCSchemaV3ControlOperations(t *testing.T) {
	configPath := t.TempDir() + `\sondns.json`
	engine := &DNSProxyEngine{config: DefaultConfig(), configPath: configPath, startedAt: time.Now()}
	server := newDNSIPCServer(NewDNSRequestRing(), nil)
	server.engine = engine
	state := server.respond(dnsIPCRequest{Op: "get_state"})
	if !state.OK || state.SchemaVersion != 3 || state.State == nil {
		t.Fatalf("get_state response: %+v", state)
	}
	config := server.respond(dnsIPCRequest{Op: "get_config"})
	if !config.OK || config.Config == nil || config.Config.Provider != ProviderSonCMS {
		t.Fatalf("get_config response: %+v", config)
	}
	dpi := server.respond(dnsIPCRequest{Op: "set_dpi", Enabled: boolPtr(true), CaptureMode: "tcp+udp"})
	if !dpi.OK || !dpi.NeedsRestart || !engine.config.DPI.Enabled || engine.config.DPI.CaptureMode != "tcp+udp" {
		t.Fatalf("set_dpi response: %+v config=%+v", dpi, engine.config)
	}
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("set_dpi did not persist config: %v", err)
	}
	bad := server.respond(dnsIPCRequest{Op: "unknown"})
	if bad.OK || bad.Code != "UNKNOWN_OPERATION" || bad.SchemaVersion != 3 {
		t.Fatalf("unknown operation response: %+v", bad)
	}
}

func boolPtr(value bool) *bool { return &value }

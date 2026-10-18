//go:build windows

package control

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "sondns/internal/config"
	"sondns/internal/requestlog"
)

type testEngineProxy struct {
	mu        sync.Mutex
	upstreams []appconfig.Upstream
}

func (p *testEngineProxy) SetUpstreams(_ context.Context, items []appconfig.Upstream) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.upstreams = items
	return nil
}

func (p *testEngineProxy) snapshot() []appconfig.Upstream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]appconfig.Upstream(nil), p.upstreams...)
}

func TestDNSIPCServerRoundTrip(t *testing.T) {
	ring := requestlog.New()
	proxy := &testEngineProxy{}
	server := NewServer(ring, proxy)
	server.PipeName = fmt.Sprintf(`\\.\pipe\SonDNS-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	call := func(req Request) (Response, error) { return CallAt(server.PipeName, req) }
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ring.Add(requestlog.Entry{Domain: "example.com.", Type: "A", Result: "NOERROR", AnswerIPs: []string{"192.0.2.1"}})
	var response Response
	var err error
	for i := 0; i < 20; i++ {
		response, err = call(Request{Op: "snapshot"})
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
	if _, err = call(Request{Op: "set_upstream", Upstreams: []appconfig.Upstream{appconfig.PredefinedUpstream(appconfig.ProviderNextDNS)}}); err != nil || len(proxy.snapshot()) != 1 || proxy.snapshot()[0].URL != appconfig.URLNextDNS {
		t.Fatalf("set_upstream failed: %v, proxy upstreams: %+v", err, proxy.snapshot())
	}
	response, err = call(Request{Op: "clear_log"})
	if err != nil || len(response.Entries) != 0 {
		t.Fatalf("log clear failed: %+v %v", response, err)
	}
}

func TestLiveServiceIPC(t *testing.T) {
	resp, err := Call(Request{Op: "snapshot"})
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
	resp, err := Call(Request{
		Op:        "set_upstream",
		Upstreams: []appconfig.Upstream{appconfig.PredefinedUpstream(appconfig.ProviderNextDNS)},
	})
	if err != nil {
		t.Skip("SonDNS service not running")
	}
	if !resp.OK {
		t.Fatalf("failed to switch to NextDNS: %s", resp.Error)
	}

	resp, err = Call(Request{
		Op:        "set_upstream",
		Upstreams: []appconfig.Upstream{appconfig.PredefinedUpstream(appconfig.ProviderSonCMS)},
	})
	if err != nil || !resp.OK {
		t.Fatalf("failed to switch to SonCMS: %v, resp: %+v", err, resp)
	}
}

func TestDNSIPCSnapshotVersionSkip(t *testing.T) {
	ring := requestlog.New()
	proxy := &testEngineProxy{}
	server := NewServer(ring, proxy)

	ring.Add(requestlog.Entry{Domain: "example.org", Type: "A", Result: "NOERROR"})
	v := ring.Version()
	resp := server.respond(Request{Op: "snapshot", Version: v, InstanceID: server.instanceID})
	if !resp.Unchanged || len(resp.Entries) != 0 || resp.Version != v {
		t.Fatalf("unchanged snapshot: %+v", resp)
	}
	resp = server.respond(Request{Op: "snapshot", Version: v, InstanceID: "previous-service"})
	if resp.Unchanged || len(resp.Entries) != 1 {
		t.Fatalf("version collision after restart suppressed rows: %+v", resp)
	}
	ring.Clear()
	resp = server.respond(Request{Op: "snapshot", Version: v, InstanceID: server.instanceID})
	if resp.Unchanged || len(resp.Entries) != 0 || resp.Version <= v {
		t.Fatalf("clear must return a changed, empty snapshot: %+v", resp)
	}
}

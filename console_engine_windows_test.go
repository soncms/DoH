//go:build windows && console

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/ameshkov/dnsstamps"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
)

type consoleFixture struct {
	calls atomic.Int64
	do    atomic.Bool
	delay atomic.Bool
}

func (*consoleFixture) Address() string { return "fixture" }
func (*consoleFixture) Close() error    { return nil }
func (f *consoleFixture) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	f.calls.Add(1)
	f.do.Store(q.IsEdns0() != nil && q.IsEdns0().Do())
	if f.delay.Load() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(600 * time.Millisecond):
		}
	}
	r := new(dns.Msg)
	r.SetReply(q)
	r.AuthenticatedData = true
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 5)}}
	if f.do.Load() {
		r.SetEdns0(1232, true)
	}
	return r, nil
}

func consoleTestLock(t *testing.T) (string, componentLock) {
	t.Helper()
	exe := os.Getenv("SONDNS_DNSPROXY_TEST_EXE")
	if exe == "" {
		t.Skip("set SONDNS_DNSPROXY_TEST_EXE to the official Windows amd64 dnsproxy.exe")
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	l := componentLock{Schema: 1, Components: map[string]installedComponent{}}
	for name, files := range map[string][]string{"dnsproxy": {"dnsproxy.exe"}, "windivert": {"WinDivert.dll", "WinDivert64.sys"}, "zapret2": {"winws2.exe", "cygwin1.dll", "WinDivert.dll", "WinDivert64.sys", "lua/zapret-lib.lua", "lua/zapret-antidpi.lua"}} {
		c := installedComponent{Dir: "runtime/" + name + "/test", Files: map[string]string{}}
		for _, f := range files {
			data := []byte("fixture")
			if name == "dnsproxy" {
				data = b
			}
			if err := writeAtomic(filepath.Join(root, filepath.FromSlash(c.Dir), filepath.FromSlash(f)), data); err != nil {
				t.Fatal(err)
			}
			c.Files[f] = hashBytes(data)
		}
		l.Components[name] = c
	}
	return root, l
}

func TestConsoleOfficialTransportsAndLifecycle(t *testing.T) {
	root, lock := consoleTestLock(t)
	f := &consoleFixture{}
	var httpVersion atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpVersion.Store(int64(r.ProtoMajor))
		var wire []byte
		var err error
		if r.Method == "GET" {
			wire, err = base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		} else {
			wire, err = io.ReadAll(io.LimitReader(r.Body, 65536))
		}
		if err != nil {
			http.Error(w, "bad DNS", 400)
			return
		}
		q := new(dns.Msg)
		if q.Unpack(wire) != nil {
			http.Error(w, "bad DNS", 400)
			return
		}
		res, err := f.Exchange(r.Context(), q)
		if err != nil {
			return
		}
		wire, _ = res.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	})
	h2 := httptest.NewUnstartedServer(h)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	defer h2.Close()
	p, err := proxy.New(&proxy.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), TLSConfig: &tls.Config{Certificates: h2.TLS.Certificates, MinVersion: tls.VersionTLS12}, TLSListenAddr: []*net.TCPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, QUICListenAddr: []*net.UDPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, UpstreamConfig: &proxy.UpstreamConfig{Upstreams: []upstream.Upstream{f}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(context.Background())
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h3 := &http3.Server{TLSConfig: &tls.Config{Certificates: h2.TLS.Certificates}, Handler: h}
	go h3.Serve(packet)
	defer h3.Close()
	for _, tc := range []struct {
		name, url string
		version   int64
	}{{"DoH_HTTP2", h2.URL + "/dns-query", 2}, {"DoH_pinned_hostname", strings.Replace(h2.URL, "127.0.0.1", "fixture.invalid", 1) + "/dns-query?profile=test", 2}, {"DoH_HTTP3", "https://" + packet.LocalAddr().String() + "/dns-query", 3}, {"DoT", "tls://" + p.Addr(proxy.ProtoTLS).String(), 0}, {"DoQ", "quic://" + p.Addr(proxy.ProtoQUIC).String(), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			c.Listen = "127.0.0.1:0"
			c.Upstreams = []Upstream{{URL: tc.url}}
			if tc.name == "DoH_pinned_hostname" {
				c.Upstreams[0].Bootstrap = []string{h2.Listener.Addr().String()}
			}
			c.Timeout = 3 * time.Second
			var helperLog bytes.Buffer
			e := newConsoleEngine(c, root, lock, log.New(&helperLog, "", 0))
			e.pipe = consolePipe(root, true) + tc.name
			e.extraArgs = []string{"--insecure"}
			if err := e.Start(context.Background()); err != nil {
				t.Fatalf("%v\n%s", err, helperLog.String())
			}
			defer e.Shutdown(context.Background())
			q := new(dns.Msg)
			q.SetQuestion("fixture.example.", dns.TypeA)
			before := f.calls.Load()
			for _, network := range []string{"udp", "tcp"} {
				res, _, err := (&dns.Client{Net: network, Timeout: 3 * time.Second}).Exchange(q, e.cfg.Listen)
				if err != nil || res == nil || len(res.Answer) != 1 || res.Id != q.Id {
					t.Fatalf("%s: %v %v", network, res, err)
				}
			}
			if delta := f.calls.Load() - before; delta != 2 {
				t.Fatalf("cache/pending requests must be off, calls=%d", delta)
			}
			if tc.version != 0 && httpVersion.Load() != tc.version {
				t.Fatalf("HTTP version=%d want %d", httpVersion.Load(), tc.version)
			}
			for _, enabled := range []bool{true, false} {
				item := c.Upstreams[0]
				item.DNSSEC = enabled
				if err := e.SetUpstreams(context.Background(), []Upstream{item}); err != nil {
					t.Fatal(err)
				}
				for _, clientDO := range []bool{true, false} {
					for _, ad := range []bool{true, false} {
						q := new(dns.Msg)
						q.SetQuestion("dnssec.example.", dns.TypeA)
						q.SetEdns0(1232, clientDO)
						q.AuthenticatedData = ad
						r, err := e.ResolveMsg(context.Background(), q)
						if err != nil {
							t.Fatal(err)
						}
						if f.do.Load() != (enabled || clientDO) {
							t.Fatalf("upstream DO enabled=%v client=%v got=%v", enabled, clientDO, f.do.Load())
						}
						if r.AuthenticatedData != (clientDO || ad) {
							t.Fatalf("AD response=%v DO=%v AD=%v", r.AuthenticatedData, clientDO, ad)
						}
						if q.IsEdns0().Do() != clientDO {
							t.Fatal("mutated client query")
						}
					}
				}
			}
			if err := e.SetUpstreams(context.Background(), []Upstream{{URL: "udp://1.1.1.1"}}); err == nil {
				t.Fatal("accepted plaintext")
			}
			f.delay.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			at := time.Now()
			_, err := e.ResolveMsg(ctx, q)
			cancel()
			f.delay.Store(false)
			if err == nil || time.Since(at) > 350*time.Millisecond {
				t.Fatalf("cancellation was not prompt: %v", err)
			}
			old := e.generation.child
			old.stop()
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				e.mu.RLock()
				alive := e.generation.child != old && e.generation.child.alive()
				e.mu.RUnlock()
				if alive {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			e.mu.RLock()
			alive := e.generation.child != old && e.generation.child.alive()
			e.mu.RUnlock()
			if !alive {
				t.Fatal("helper was not restarted")
			}
			if err := e.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			if e.generation.child.alive() {
				t.Fatal("helper leaked")
			}
		})
	}
}

func TestConsoleBootstrapStampAndResponsePolicy(t *testing.T) {
	c := DefaultConfig()
	c.Upstreams = []Upstream{{URL: "https://resolver.example:8443/dns-query?profile=abc", Bootstrap: []string{"192.0.2.1:8443"}}}
	a, err := officialUpstreamArgs(context.Background(), c, log.Default())
	if err != nil {
		t.Fatal(err)
	}
	s, err := dnsstamps.NewServerStampFromString(strings.TrimPrefix(a[0], "--upstream="))
	if err != nil {
		t.Fatal(err)
	}
	if s.ServerAddrStr != "192.0.2.1:8443" || s.ProviderName != "resolver.example:8443" || s.Path != "/dns-query?profile=abc" {
		t.Fatalf("incorrect stamp: %+v", s)
	}
	for _, qt := range []uint16{dns.TypeA, dns.TypeDNSKEY} {
		q := new(dns.Msg)
		q.SetQuestion("example.", qt)
		r := new(dns.Msg)
		r.SetReply(q)
		r.AuthenticatedData = true
		r.SetEdns0(1232, true)
		r.Answer = []dns.RR{&dns.DNSKEY{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeDNSKEY, Class: 1}}}
		restoreClientDNSFlags(q, r)
		if r.IsEdns0() != nil || r.AuthenticatedData {
			t.Fatal("leaked DO/AD")
		}
		want := 0
		if qt == dns.TypeDNSKEY {
			want = 1
		}
		if len(r.Answer) != want {
			t.Fatal("explicit DNSKEY policy")
		}
	}
}

func TestConsoleRuntimeIntegrityAndMenu(t *testing.T) {
	root, lock := consoleTestLock(t)
	if err := verifyInstalled(root, lock); err != nil {
		t.Fatal(err)
	}
	c := lock.Components["dnsproxy"]
	c.Dir = "../escape"
	if _, err := componentPath(root, c); err == nil {
		t.Fatal("accepted path traversal")
	}
	c = lock.Components["dnsproxy"]
	p := filepath.Join(root, filepath.FromSlash(c.Dir), "dnsproxy.exe")
	if err := os.WriteFile(p, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyInstalled(root, lock); err == nil {
		t.Fatal("accepted modified artifact")
	}
	for _, input := range []string{"0\n", "", "9\n0\n"} {
		var out strings.Builder
		if err := consoleMenu(root, strings.NewReader(input), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Components") {
			t.Fatal(fmt.Sprint("missing menu: ", out.String()))
		}
	}
}

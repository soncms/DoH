//go:build !console

package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/ameshkov/dnsstamps"
	"github.com/miekg/dns"
)

func regressionEngine(t *testing.T, enabled bool) *DNSProxyEngine {
	t.Helper()
	c := DefaultConfig()
	c.Listen = "127.0.0.1:0"
	c.Upstreams = []Upstream{{URL: "https://resolver.invalid/dns-query", Bootstrap: []string{"127.0.0.1:443"}, DNSSEC: enabled}}
	e, _, err := NewDNSProxyEngine(c, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.upstreams.Close() })
	return e
}

func TestConfigAndHotSwapShareEncryptedValidation(t *testing.T) {
	e := regressionEngine(t, false)
	plain := dnsstamps.ServerStamp{Proto: dnsstamps.StampProtoTypePlain, ServerAddrStr: "127.0.0.1:53"}
	for _, endpoint := range []string{"udp://127.0.0.1:53", "tcp://127.0.0.1:53", "127.0.0.1:53", plain.String(), "sdns://broken"} {
		t.Run(endpoint, func(t *testing.T) {
			items := []Upstream{{URL: endpoint, Bootstrap: []string{"127.0.0.1:443"}}}
			c := DefaultConfig()
			c.Upstreams = items
			before := e.upstreams.Upstreams[0]
			if err := c.normalize(); err == nil {
				t.Fatal("config accepted plaintext or malformed upstream")
			}
			if err := e.SetUpstreams(context.Background(), items); err == nil {
				t.Fatal("hot-swap accepted plaintext or malformed upstream")
			}
			if e.upstreams.Upstreams[0] != before {
				t.Fatal("failed validation changed active upstream")
			}
		})
	}
	for _, proto := range []dnsstamps.StampProtoType{dnsstamps.StampProtoTypeDNSCrypt, dnsstamps.StampProtoTypeDoH, dnsstamps.StampProtoTypeTLS, dnsstamps.StampProtoTypeDoQ} {
		stamp := dnsstamps.ServerStamp{Proto: proto, ServerAddrStr: "127.0.0.1:443", ProviderName: "resolver.invalid", Path: "/dns-query", ServerPk: make([]byte, 32)}
		if _, err := normalizeUpstreams([]Upstream{{URL: stamp.String()}}); err != nil {
			t.Fatalf("encrypted stamp %v rejected: %v", proto, err)
		}
	}
}

type dnssecProbe struct{ gotDO atomic.Bool }

func (*dnssecProbe) Address() string { return "dnssec-probe" }
func (*dnssecProbe) Close() error    { return nil }
func (p *dnssecProbe) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	p.gotDO.Store(q.IsEdns0() != nil && q.IsEdns0().Do())
	r := new(dns.Msg)
	r.SetReply(q)
	r.AuthenticatedData = true
	r.SetEdns0(1232, true)
	r.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 1)},
		&dns.RRSIG{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 60}, TypeCovered: dns.TypeA},
	}
	return r, nil
}

func TestDNSSECStartupAndHotSwapWithCacheDisabled(t *testing.T) {
	e := regressionEngine(t, false)
	// First iteration covers startup, subsequent iterations cover both directions.
	for index, enabled := range []bool{false, true, false, true} {
		if index > 0 {
			if err := e.SetUpstreams(context.Background(), []Upstream{{URL: "https://resolver.invalid/dns-query", Bootstrap: []string{"127.0.0.1:443"}, DNSSEC: enabled}}); err != nil {
				t.Fatal(err)
			}
		}
		wrapped, ok := e.upstreams.Upstreams[0].(*dnssecUpstream)
		if !ok {
			t.Fatal("production upstream has no DNSSEC adapter")
		}
		_ = wrapped.Upstream.Close()
		probe := &dnssecProbe{}
		wrapped.Upstream = probe
		for _, clientDO := range []bool{false, true} {
			for _, clientAD := range []bool{false, true} {
				for _, direct := range []bool{false, true} {
					q := new(dns.Msg)
					q.SetQuestion("signed.example.", dns.TypeA)
					q.SetEdns0(1232, clientDO)
					q.AuthenticatedData = clientAD
					var res *dns.Msg
					var err error
					if direct {
						res, err = e.ResolveMsg(context.Background(), q)
					} else {
						dctx := &dnsproxy.DNSContext{Req: q.Copy(), Proto: dnsproxy.ProtoUDP}
						err = (requestLogHandler{ring: e.requests, configMu: &e.mu}).ServeDNS(context.Background(), e.proxy, dctx)
						res = dctx.Res
					}
					if err != nil {
						t.Fatal(err)
					}
					if probe.gotDO.Load() != (enabled || clientDO) {
						t.Fatalf("generation=%d enabled=%v clientDO=%v upstreamDO=%v", index, enabled, clientDO, probe.gotDO.Load())
					}
					if got := len(res.Answer); got != map[bool]int{false: 1, true: 2}[clientDO] {
						t.Fatalf("DNSSEC response filtering: DO=%v answer=%v", clientDO, res.Answer)
					}
					if res.AuthenticatedData != (clientAD || clientDO) {
						t.Fatalf("AD flag not filtered for original client flags: %+v", res)
					}
					if q.IsEdns0().Do() != clientDO {
						t.Fatal("caller query mutated")
					}
				}
			}
		}
	}
}

func TestCanceledSwapKeepsActiveGeneration(t *testing.T) {
	e := regressionEngine(t, false)
	before := e.upstreams.Upstreams[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.SetUpstreams(ctx, []Upstream{PredefinedUpstream(ProviderNextDNS)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled swap: %v", err)
	}
	if e.upstreams.Upstreams[0] != before {
		t.Fatal("canceled swap replaced active generation")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := e.SetUpstreams(ctx, []Upstream{PredefinedUpstream(ProviderNextDNS)}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked swap ignored context: %v", err)
	}
	if time.Since(started) > time.Second || e.upstreams.Upstreams[0] != before {
		t.Fatal("timed-out swap changed or held the generation")
	}
}

func TestBootstrapBoundsConcurrencyAndCancelsLosers(t *testing.T) {
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	entered := make(chan struct{}, 32)
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		entered <- struct{}{}
		<-gate
		r := new(dns.Msg)
		r.SetReply(q)
		if q.Question[0].Qtype == dns.TypeA {
			r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 6)}}
		}
		_ = w.WriteMsg(r)
	})
	server := &dns.Server{PacketConn: packet, Handler: handler}
	go server.ActivateAndServe()
	t.Cleanup(func() { _ = server.Shutdown() })
	servers := make([]string, 12)
	for i := range servers {
		servers[i] = packet.LocalAddr().String()
	}
	done := make(chan bool, 1)
	go func() {
		ips, _ := raceBootstrapServers(context.Background(), servers, "bounded.example.", time.Second)
		done <- len(ips) > 0
	}()
	for i := 0; i < bootstrapConcurrency; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			close(gate)
			t.Fatal("bootstrap workers did not start")
		}
	}
	select {
	case <-entered:
		close(gate)
		t.Fatal("more than four bootstrap servers queried concurrently")
	case <-time.After(30 * time.Millisecond):
	}
	close(gate)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("winning bootstrap result lost")
		}
	case <-time.After(time.Second):
		t.Fatal("losing bootstrap lookups did not cancel")
	}
}

func TestBootstrapCancellationAndTCPFallback(t *testing.T) {
	packet, tcp := listenTestDNSPair(t)
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		if strings.HasPrefix(q.Question[0].Name, "stall.") {
			return
		}
		r := new(dns.Msg)
		r.SetReply(q)
		if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
			r.Truncated = true
		} else if q.Question[0].Qtype == dns.TypeA {
			r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET}, A: net.IPv4(192, 0, 2, 3)}}
		}
		_ = w.WriteMsg(r)
	})
	udpServer := &dns.Server{PacketConn: packet, Handler: handler}
	tcpServer := &dns.Server{Listener: tcp, Handler: handler}
	go udpServer.ActivateAndServe()
	go tcpServer.ActivateAndServe()
	t.Cleanup(func() { _ = udpServer.Shutdown(); _ = tcpServer.Shutdown() })
	ips, err := resolveHostViaBootstrapDNS(context.Background(), []string{tcp.Addr().String()}, "fallback.example", time.Second, nil)
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.3" {
		t.Fatalf("TCP fallback: %v %v", ips, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(40*time.Millisecond, cancel)
	started := time.Now()
	_, err = resolveHostViaBootstrapDNS(ctx, []string{tcp.Addr().String()}, "stall.example", 5*time.Second, nil)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("bootstrap cancellation delayed: %s %v", time.Since(started), err)
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

func TestRequestResultClassifiesTransportFailures(t *testing.T) {
	servfail := new(dns.Msg)
	servfail.Rcode = dns.RcodeServerFailure
	if got := requestResult(&net.DNSError{IsTimeout: true}, servfail, nil); got != "TIMEOUT" {
		t.Fatalf("timeout result = %q", got)
	}
	if got := requestResult(errors.New("connection refused"), servfail, nil); got != "ERROR" {
		t.Fatalf("transport result = %q", got)
	}
	nxdomain := new(dns.Msg)
	nxdomain.Rcode = dns.RcodeNameError
	if got := requestResult(nil, nxdomain, nil); got != "NXDOMAIN" {
		t.Fatalf("rcode result = %q", got)
	}
}

type blockingUpstream struct {
	entered           chan struct{}
	release           chan struct{}
	active            atomic.Bool
	closedWhileActive atomic.Bool
}

func (u *blockingUpstream) Address() string { return "blocking" }
func (u *blockingUpstream) Close() error {
	u.closedWhileActive.Store(u.active.Load())
	return nil
}
func (u *blockingUpstream) Exchange(req *dns.Msg) (*dns.Msg, error) {
	u.active.Store(true)
	close(u.entered)
	<-u.release
	u.active.Store(false)
	res := new(dns.Msg)
	res.SetReply(req)
	return res, nil
}

func TestProviderSwapWaitsForActiveQueries(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(map[bool]string{true: "interceptor", false: "listener"}[direct], func(t *testing.T) {
			u := &blockingUpstream{entered: make(chan struct{}), release: make(chan struct{})}
			conf := &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}}
			p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: conf})
			if err != nil {
				t.Fatal(err)
			}
			e := &DNSProxyEngine{proxy: p, upstreams: conf, requests: NewDNSRequestRing()}
			done := make(chan error, 1)
			go func() {
				msg := new(dns.Msg)
				msg.SetQuestion("in-flight.example.", dns.TypeA)
				if direct {
					_, err := e.ResolveMsg(context.Background(), msg)
					done <- err
				} else {
					done <- (requestLogHandler{ring: e.requests, configMu: &e.mu}).ServeDNS(context.Background(), p, &dnsproxy.DNSContext{Req: msg, Proto: dnsproxy.ProtoUDP})
				}
			}()
			<-u.entered
			swapped := make(chan struct{})
			go func() {
				e.replaceUpstreams(&dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{&countingUpstream{}}})
				close(swapped)
			}()
			select {
			case <-swapped:
				t.Error("provider changed while a request was still using it")
			case <-time.After(25 * time.Millisecond):
			}
			close(u.release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			select {
			case <-swapped:
			case <-time.After(time.Second):
				t.Fatal("provider swap did not finish")
			}
			if u.closedWhileActive.Load() {
				t.Fatal("closed an in-flight upstream")
			}
			msg := new(dns.Msg)
			msg.SetQuestion("after-swap.example.", dns.TypeA)
			res, err := e.ResolveMsg(context.Background(), msg)
			if err != nil || len(res.Answer) != 1 {
				t.Fatalf("new provider not used: %v %+v", err, res)
			}
		})
	}
}

func BenchmarkResolverUpstream(b *testing.B) {
	conf := &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{&countingUpstream{}}}
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: conf})
	if err != nil {
		b.Fatal(err)
	}
	r := &Resolver{engine: &DNSProxyEngine{proxy: p, upstreams: conf, requests: NewDNSRequestRing()}}
	msg := new(dns.Msg)
	msg.SetQuestion("repeated.example.", dns.TypeA)
	wire, _ := msg.Pack()
	if _, err := r.Resolve(context.Background(), wire); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Resolve(context.Background(), wire); err != nil {
			b.Fatal(err)
		}
	}
}

type countingUpstream struct{ calls int }

func (u *countingUpstream) Address() string { return "mock" }
func (u *countingUpstream) Close() error    { return nil }
func (u *countingUpstream) Exchange(req *dns.Msg) (*dns.Msg, error) {
	u.calls++
	if req.Question[0].Name == "fail.example." {
		return nil, errors.New("offline")
	}
	response := new(dns.Msg)
	response.SetReply(req)
	response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 1)}}
	return response, nil
}
func TestRequestHandlerAlwaysUsesUpstreamAndLogsFailuresAndIPs(t *testing.T) {
	u := &countingUpstream{}
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}}})
	if err != nil {
		t.Fatal(err)
	}
	ring := NewDNSRequestRing()
	h := requestLogHandler{ring: ring}
	for _, name := range []string{"repeated.example.", "repeated.example.", "fail.example."} {
		msg := new(dns.Msg)
		msg.SetQuestion(name, dns.TypeA)
		_ = h.ServeDNS(context.Background(), p, &dnsproxy.DNSContext{Req: msg, Proto: dnsproxy.ProtoUDP})
	}
	if u.calls != 3 {
		t.Fatalf("upstream calls=%d", u.calls)
	}
	rows, _ := ring.Snapshot()
	if len(rows) != 3 || rows[0].AnswerIPs[0] != "192.0.2.1" || rows[1].Result != "NOERROR" || rows[2].Result == "NOERROR" {
		t.Fatalf("invalid records: %+v", rows)
	}
	if rows[0].Upstream != "mock" || rows[1].Upstream != "mock" {
		t.Fatalf("upstream metadata is wrong: first=%+v second=%+v", rows[0], rows[1])
	}
}

type timeoutUpstream struct{}

func (timeoutUpstream) Address() string { return "timeout" }
func (timeoutUpstream) Close() error    { return nil }
func (timeoutUpstream) Exchange(*dns.Msg) (*dns.Msg, error) {
	return nil, &net.DNSError{Err: "i/o timeout", IsTimeout: true}
}

func TestRequestHandlerLogsTimeout(t *testing.T) {
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{timeoutUpstream{}}}})
	if err != nil {
		t.Fatal(err)
	}
	ring := NewDNSRequestRing()
	msg := new(dns.Msg)
	msg.SetQuestion("timeout.example.", dns.TypeA)
	_ = (requestLogHandler{ring: ring}).ServeDNS(context.Background(), p, &dnsproxy.DNSContext{Req: msg, Proto: dnsproxy.ProtoUDP})
	rows, _ := ring.Snapshot()
	if len(rows) != 1 || rows[0].Result != "TIMEOUT" || rows[0].Error == "" {
		t.Fatalf("timeout was not recorded: %+v", rows)
	}
}

func TestDirectResolveMsgSuccessAndFailure(t *testing.T) {
	u := &countingUpstream{}
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}}})
	if err != nil {
		t.Fatal(err)
	}
	ring := NewDNSRequestRing()
	engine := &DNSProxyEngine{proxy: p, requests: ring}

	// 1. Successful query
	msg := new(dns.Msg)
	msg.SetQuestion("direct.example.", dns.TypeA)
	res, err := engine.ResolveMsg(context.Background(), msg)
	if err != nil || res == nil || len(res.Answer) == 0 {
		t.Fatalf("expected successful answer, got err=%v res=%+v", err, res)
	}
	if res.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatalf("unexpected IP: %v", res.Answer[0])
	}

	// 2. Failing query returns SERVFAIL with RecursionAvailable
	failMsg := new(dns.Msg)
	failMsg.SetQuestion("fail.example.", dns.TypeA)
	resFail, _ := engine.ResolveMsg(context.Background(), failMsg)
	if resFail == nil {
		t.Fatal("expected non-nil response for failure")
	}
	if resFail.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL, got %d", resFail.Rcode)
	}
	if !resFail.RecursionAvailable {
		t.Fatal("expected RecursionAvailable to be true")
	}
	rows, _ := ring.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("one client query must log once, not once per retry: %d rows", len(rows))
	}
	if rows[1].AttemptCount != 2 || len(rows[1].Attempts) != 2 || rows[1].RCode != "SERVFAIL" {
		t.Fatalf("retry lifecycle metadata is wrong: %+v", rows[1])
	}
	resolver := &Resolver{engine: engine}
	wire, _ := failMsg.Pack()
	reply, err := resolver.Resolve(context.Background(), wire)
	if err != nil {
		t.Fatalf("resolver dropped synthetic SERVFAIL: %v", err)
	}
	decoded := new(dns.Msg)
	if err := decoded.Unpack(reply); err != nil || decoded.Rcode != dns.RcodeServerFailure || decoded.Id != failMsg.Id {
		t.Fatalf("invalid SERVFAIL reply: %v, %+v", err, decoded)
	}
}

func TestDirectResolveMsgCanceledBeforeFirstAttemptStillLogsOnce(t *testing.T) {
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{&countingUpstream{}}}})
	if err != nil {
		t.Fatal(err)
	}
	ring := NewDNSRequestRing()
	engine := &DNSProxyEngine{proxy: p, requests: ring}
	msg := new(dns.Msg)
	msg.SetQuestion("canceled.example.", dns.TypeAAAA)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = engine.ResolveMsg(ctx, msg)
	rows, _ := ring.Snapshot()
	if len(rows) != 1 || rows[0].Result != "ERROR" || rows[0].Error == "" {
		t.Fatalf("canceled request was not finalized once: %+v", rows)
	}
}

// Exercise production construction with a legacy config that used to enable cache.
func TestEngineIgnoresLegacyCacheAndAlwaysQueriesUpstream(t *testing.T) {
	c := DefaultConfig()
	if err := json.Unmarshal([]byte(`{"cache_enabled":true,"cache_size_bytes":67108864}`), &c); err != nil {
		t.Fatal(err)
	}
	c.Upstreams = []Upstream{PredefinedUpstream(ProviderNextDNS)}
	e, _, err := NewDNSProxyEngine(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := &countingUpstream{}
	e.replaceUpstreams(&dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}})
	defer e.upstreams.Close()
	for i := 0; i < 4; i++ {
		msg := new(dns.Msg)
		msg.SetQuestion("repeated.example.", dns.TypeA)
		if i%2 == 0 {
			res, err := e.ResolveMsg(context.Background(), msg)
			if err != nil || res == nil || len(res.Answer) != 1 {
				t.Fatalf("direct resolve: %v %v", res, err)
			}
		} else {
			dctx := &dnsproxy.DNSContext{Req: msg, Proto: dnsproxy.ProtoUDP}
			if err := (requestLogHandler{ring: e.requests, configMu: &e.mu}).ServeDNS(context.Background(), e.proxy, dctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if u.calls != 4 {
		t.Fatalf("upstream calls = %d, want 4", u.calls)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["cache_enabled"]; ok {
		t.Fatal("obsolete cache setting persisted")
	}
}

package main

import (
	"context"
	"errors"
	"net"
	"testing"

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
func TestRequestHandlerLogsCacheHitsFailuresAndIPs(t *testing.T) {
	u := &countingUpstream{}
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}}, CacheEnabled: true, CacheSizeBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	ring := NewDNSRequestRing()
	h := requestLogHandler{ring: ring}
	for _, name := range []string{"cached.example.", "cached.example.", "fail.example."} {
		msg := new(dns.Msg)
		msg.SetQuestion(name, dns.TypeA)
		_ = h.ServeDNS(context.Background(), p, &dnsproxy.DNSContext{Req: msg, Proto: dnsproxy.ProtoUDP})
	}
	if u.calls != 2 {
		t.Fatalf("upstream calls=%d", u.calls)
	}
	rows, _ := ring.Snapshot()
	if len(rows) != 3 || rows[0].AnswerIPs[0] != "192.0.2.1" || rows[1].Result != "NOERROR" || rows[2].Result == "NOERROR" {
		t.Fatalf("invalid records: %+v", rows)
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
	p, err := dnsproxy.New(&dnsproxy.Config{UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{u}}, CacheEnabled: true, CacheSizeBytes: 1 << 20})
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
}

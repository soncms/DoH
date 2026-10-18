//go:build !console

package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/dnscrypt"
	"github.com/miekg/dns"
)

// A locally pinned encrypted upstream for disposable-machine service tests.
// No public resolver, system trust-store change or TLS-verification bypass is
// needed. It handles only synthetic .invalid questions and never forwards them.
func startLocalEncryptedDNSFixture(t *testing.T) (stamp string, calls *atomic.Int64) {
	t.Helper()
	rc, err := dnscrypt.GenerateResolverConfig("sondns-fixture.invalid", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := rc.NewCert()
	if err != nil {
		t.Fatal(err)
	}
	calls = new(atomic.Int64)
	handler := dnscrypt.HandlerFunc(func(ctx context.Context, w dnscrypt.ResponseWriter, q *dns.Msg) error {
		calls.Add(1)
		return w.WriteMsg(ctx, encryptedFixtureReply(q))
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var address string
	for _, protocol := range []dnscrypt.Proto{dnscrypt.ProtoUDP, dnscrypt.ProtoTCP} {
		addr := netip.MustParseAddrPort("127.0.0.1:0")
		if address != "" {
			addr = netip.MustParseAddrPort(address)
		}
		server, err := dnscrypt.NewServer(&dnscrypt.ServerConfig{ProviderName: rc.ProviderName, ResolverCert: cert,
			Addr: addr, Proto: protocol, Handler: handler, Logger: logger})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				t.Errorf("fixture shutdown: %v", err)
			}
		})
		address = server.LocalAddr().String()
	}
	pin, err := rc.CreateStamp(address)
	if err != nil {
		t.Fatal(err)
	}
	return pin.String(), calls
}

func encryptedFixtureReply(q *dns.Msg) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(q)
	if len(q.Question) != 1 || q.Question[0].Name != "sondns-test.invalid." {
		r.Rcode = dns.RcodeNameError
		return r
	}
	hdr := dns.RR_Header{Name: q.Question[0].Name, Rrtype: q.Question[0].Qtype, Class: dns.ClassINET, Ttl: 0}
	switch q.Question[0].Qtype {
	case dns.TypeA:
		r.Answer = []dns.RR{&dns.A{Hdr: hdr, A: net.ParseIP("192.0.2.5")}}
	case dns.TypeAAAA:
		r.Answer = []dns.RR{&dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::5")}}
	}
	return r
}

func requireEncryptedFixtureReply(t *testing.T, q, r *dns.Msg) {
	t.Helper()
	if r == nil || !r.Response || r.Id != q.Id || r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 {
		t.Fatalf("unexpected fixture reply: %v", r)
	}
	var address string
	switch answer := r.Answer[0].(type) {
	case *dns.A:
		address = answer.A.String()
	case *dns.AAAA:
		address = answer.AAAA.String()
	default:
		t.Fatalf("unexpected answer type %T", answer)
	}
	want := "192.0.2.5"
	if q.Question[0].Qtype == dns.TypeAAAA {
		want = "2001:db8::5"
	}
	if address != want {
		t.Fatalf("reply came from a different upstream: %s != %s", address, want)
	}
}

func TestLocalPinnedDNSCryptFixture(t *testing.T) {
	stamp, calls := startLocalEncryptedDNSFixture(t)
	c := DefaultConfig()
	c.Upstreams = []Upstream{{URL: stamp}}
	e, _, err := NewDNSProxyEngine(c, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer e.upstreams.Close()
	for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeA, dns.TypeAAAA} {
		q := new(dns.Msg)
		q.SetQuestion("sondns-test.invalid.", qtype)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		r, err := e.ResolveMsg(ctx, q)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		requireEncryptedFixtureReply(t, q, r)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("queries did not all reach pinned encrypted fixture: %d", got)
	}
}

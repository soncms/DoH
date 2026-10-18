//go:build !console

package testutil

import (
	"context"
	"io"
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
func StartLocalEncryptedDNSFixture(t *testing.T) (stamp string, calls *atomic.Int64) {
	t.Helper()
	return StartLocalEncryptedDNSFixtureWithGate(t, nil)
}

// beforeReply can hold a specific synthetic request until the test releases it.
// It runs after decryption, so arrival proves the request reached this upstream.
func StartLocalEncryptedDNSFixtureWithGate(t *testing.T, beforeReply func(context.Context, *dns.Msg) error) (stamp string, calls *atomic.Int64) {
	t.Helper()
	return StartLocalEncryptedDNSFixtureWithHandler(t, dnscrypt.HandlerFunc(func(ctx context.Context, w dnscrypt.ResponseWriter, q *dns.Msg) error {
		if beforeReply != nil {
			if err := beforeReply(ctx, q); err != nil {
				return err
			}
		}
		return w.WriteMsg(ctx, EncryptedFixtureReply(q))
	}))
}

func StartLocalEncryptedDNSFixtureWithHandler(t *testing.T, reply dnscrypt.Handler) (stamp string, calls *atomic.Int64) {
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
		return reply.ServeDNS(ctx, w, q)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shutdown := func(servers []*dnscrypt.Server) {
		for _, server := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := server.Shutdown(ctx)
			cancel()
			if err != nil {
				t.Errorf("fixture shutdown: %v", err)
			}
		}
	}
	var address string
	var servers []*dnscrypt.Server
	var lastErr error
	for range 32 {
		address = ""
		servers = nil
		// TCP :0 avoids ports reserved only for TCP on Windows. If UDP cannot
		// share the selected port, close this attempt and choose another pair.
		for _, protocol := range []dnscrypt.Proto{dnscrypt.ProtoTCP, dnscrypt.ProtoUDP} {
			addr := netip.MustParseAddrPort("127.0.0.1:0")
			if address != "" {
				addr = netip.MustParseAddrPort(address)
			}
			server, err := dnscrypt.NewServer(&dnscrypt.ServerConfig{ProviderName: rc.ProviderName, ResolverCert: cert,
				Addr: addr, Proto: protocol, Handler: handler, Logger: logger})
			if err == nil {
				err = server.Start(context.Background())
			}
			if err != nil {
				lastErr = err
				break
			}
			servers = append(servers, server)
			address = server.LocalAddr().String()
		}
		if len(servers) == 2 {
			break
		}
		shutdown(servers)
	}
	if len(servers) != 2 {
		t.Fatalf("cannot start loopback DNSCrypt TCP/UDP pair: %v", lastErr)
	}
	t.Cleanup(func() { shutdown(servers) })
	pin, err := rc.CreateStamp(address)
	if err != nil {
		t.Fatal(err)
	}
	return pin.String(), calls
}

func EncryptedFixtureReply(q *dns.Msg) *dns.Msg {
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

func RequireEncryptedFixtureReply(t *testing.T, q, r *dns.Msg) {
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

// Windows may reserve different port ranges for TCP and UDP. A port selected
// for one protocol is not necessarily usable for the other. Hold both sockets
// before returning, and release every partial attempt rather than skipping DNS
// coverage or changing system port reservations.
func ListenTestDNSPair(t testing.TB) (net.PacketConn, net.Listener) {
	t.Helper()
	var lastErr error
	for range 32 {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		packet, err := net.ListenPacket("udp", tcp.Addr().String())
		if err == nil {
			t.Cleanup(func() { _ = packet.Close(); _ = tcp.Close() })
			return packet, tcp
		}
		lastErr = err
		_ = tcp.Close()
	}
	t.Fatalf("cannot allocate a loopback TCP/UDP pair: %v", lastErr)
	return nil, nil
}

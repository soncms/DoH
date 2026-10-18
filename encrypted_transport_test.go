//go:build !console

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
)

type transportFixtureUpstream struct{ calls atomic.Int64 }

func (*transportFixtureUpstream) Address() string { return "local-fixture" }
func (*transportFixtureUpstream) Close() error    { return nil }
func (u *transportFixtureUpstream) Exchange(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
	u.calls.Add(1)
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(192, 0, 2, 5)}}
	return r, nil
}

func TestLocalEncryptedTransports(t *testing.T) {
	fixture := &transportFixtureUpstream{}
	var protocol atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol.Store(int64(r.ProtoMajor))
		var wire []byte
		var err error
		if r.Method == http.MethodGet {
			wire, err = base64.RawURLEncoding.DecodeString(r.URL.Query().Get("dns"))
		} else {
			wire, err = io.ReadAll(io.LimitReader(r.Body, 65536))
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		q := new(dns.Msg)
		if err = q.Unpack(wire); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, _ := fixture.Exchange(r.Context(), q)
		wire, _ = res.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	})
	h2 := httptest.NewUnstartedServer(handler)
	h2.EnableHTTP2 = true
	h2.StartTLS()
	t.Cleanup(h2.Close)
	certificate, err := x509.ParseCertificate(h2.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := dnsproxy.New(&dnsproxy.Config{
		Logger: logger, TLSConfig: &tls.Config{Certificates: h2.TLS.Certificates, MinVersion: tls.VersionTLS12},
		TLSListenAddr: []*net.TCPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, QUICListenAddr: []*net.UDPAddr{{IP: net.IPv4(127, 0, 0, 1)}},
		UpstreamConfig: &dnsproxy.UpstreamConfig{Upstreams: []upstream.Upstream{fixture}}, CacheEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h3 := &http3.Server{TLSConfig: &tls.Config{Certificates: h2.TLS.Certificates}, Handler: handler}
	go h3.Serve(packet)
	t.Cleanup(func() { _ = h3.Close(); _ = packet.Close() })
	for _, tc := range []struct {
		name, url string
		version   upstream.HTTPVersion
		wantHTTP  int64
	}{
		{"DoH_HTTP2", h2.URL + "/dns-query", upstream.HTTPVersion2, 2},
		{"DoH_HTTP3", "https://" + packet.LocalAddr().String() + "/dns-query", upstream.HTTPVersion3, 3},
		{"DoT", "tls://" + server.Addr(dnsproxy.ProtoTLS).String(), "", 0},
		{"DoQ", "quic://" + server.Addr(dnsproxy.ProtoQUIC).String(), "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := &upstream.Options{RootCAs: roots, Timeout: 2 * time.Second, Logger: logger}
			if tc.version != "" {
				options.HTTPVersions = []upstream.HTTPVersion{tc.version}
			}
			conf, err := dnsproxy.ParseUpstreamsConfig([]string{tc.url}, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conf.Close() })
			wrapDNSSECUpstreams(conf, false)
			p, err := dnsproxy.New(&dnsproxy.Config{Logger: logger, UpstreamConfig: conf, CacheEnabled: false})
			if err != nil {
				t.Fatal(err)
			}
			e := &DNSProxyEngine{proxy: p, upstreams: conf, requests: NewDNSRequestRing()}
			before := fixture.calls.Load()
			for i := 0; i < 2; i++ {
				q := new(dns.Msg)
				q.SetQuestion("encrypted.example.", dns.TypeA)
				res, err := e.ResolveMsg(context.Background(), q)
				if err != nil || res == nil || len(res.Answer) != 1 || res.Id != q.Id {
					t.Fatalf("encrypted exchange: %v %v", res, err)
				}
			}
			if fixture.calls.Load()-before != 2 {
				t.Fatal("repeated query did not reach upstream twice")
			}
			if tc.wantHTTP != 0 && protocol.Load() != tc.wantHTTP {
				t.Fatalf("negotiated HTTP/%d instead of HTTP/%d", protocol.Load(), tc.wantHTTP)
			}
			rows, _ := e.requests.Snapshot()
			if len(rows) != 2 {
				t.Fatalf("expected one log row per query: %d", len(rows))
			}
		})
	}
}

func TestUpstreamContextCancelsActiveExchange(t *testing.T) {
	entered := make(chan struct{}, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	defer server.Close()
	u, err := upstream.AddressToUpstream(server.URL+"/dns-query", &upstream.Options{InsecureSkipVerify: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		q := new(dns.Msg)
		q.SetQuestion("cancel.example.", dns.TypeA)
		_, err := u.Exchange(ctx, q)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("upstream did not start")
	}
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled exchange succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("upstream ignored cancellation")
	}
	if time.Since(started) > time.Second {
		t.Fatal("upstream cancellation delayed")
	}
}

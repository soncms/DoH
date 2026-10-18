//go:build !console

package main

import (
	"context"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/AdguardTeam/dnscrypt"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

func TestDNSCryptCancellation(t *testing.T) {
	for _, protocol := range []dnscrypt.Proto{dnscrypt.ProtoUDP, dnscrypt.ProtoTCP} {
		for _, stage := range []string{"certificate", "encrypted"} {
			t.Run(string(protocol)+"/"+stage, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				var stamp string
				if stage == "encrypted" {
					stamp, _ = startLocalEncryptedDNSFixtureWithGate(t, func(ctx context.Context, _ *dns.Msg) error {
						once.Do(func() { close(entered) })
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
				} else {
					stamp = blockedCertificateFixture(t, protocol, entered, release)
				}
				t.Cleanup(func() { close(release) })
				client := dnscrypt.NewClient(&dnscrypt.ClientConfig{Proto: protocol})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var info *dnscrypt.ResolverInfo
				if stage == "encrypted" {
					var err error
					info, err = client.DialContext(ctx, stamp)
					if err != nil {
						t.Fatal(err)
					}
				}
				done := make(chan error, 1)
				go func() {
					var err error
					if stage == "certificate" {
						_, err = client.DialContext(ctx, stamp)
					} else {
						q := new(dns.Msg)
						q.SetQuestion("sondns-test.invalid.", dns.TypeA)
						_, err = client.ExchangeContext(ctx, q, info)
					}
					done <- err
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("query did not reach the held fixture")
				}
				cancel()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("canceled DNSCrypt operation succeeded")
					}
				case <-time.After(time.Second):
					t.Fatal("DNSCrypt socket ignored cancellation while upstream remained held")
				}
			})
		}
	}
}

func TestDNSCryptTCPFallbackCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stamp, calls := startLocalEncryptedDNSFixtureWithHandler(t, dnscrypt.HandlerFunc(func(ctx context.Context, w dnscrypt.ResponseWriter, q *dns.Msg) error {
		if w.RemoteAddr().Network() == "udp" {
			r := new(dns.Msg)
			r.SetReply(q)
			r.Truncated = true
			return w.WriteMsg(ctx, r)
		}
		once.Do(func() { close(entered) })
		<-release
		return w.WriteMsg(ctx, encryptedFixtureReply(q))
	}))
	t.Cleanup(func() { close(release) })
	u, err := upstream.AddressToUpstream(stamp, &upstream.Options{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		q := new(dns.Msg)
		q.SetQuestion("sondns-test.invalid.", dns.TypeA)
		_, err := u.Exchange(ctx, q)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("encrypted TCP fallback did not reach the held fixture")
	}
	if calls.Load() != 2 {
		t.Fatalf("expected UDP attempt and TCP fallback: %d", calls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled TCP fallback succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("TCP fallback discarded the caller's cancellation")
	}
}

func TestEngineDNSCryptCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stamp, _ := startLocalEncryptedDNSFixtureWithGate(t, func(ctx context.Context, _ *dns.Msg) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	})
	t.Cleanup(func() { close(release) })
	c := DefaultConfig()
	c.Upstreams = []Upstream{{URL: stamp}}
	e, _, err := NewDNSProxyEngine(c, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer e.upstreams.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		q := new(dns.Msg)
		q.SetQuestion("sondns-test.invalid.", dns.TypeA)
		_, err := e.ResolveMsg(ctx, q)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not reach the held upstream")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled engine query succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("engine kept its packet worker in the canceled exchange")
	}
}

func blockedCertificateFixture(t *testing.T, protocol dnscrypt.Proto, entered chan struct{}, release <-chan struct{}) string {
	t.Helper()
	packet, listener := listenTestDNSPair(t)
	address := listener.Addr().String()
	var once sync.Once
	server := &dns.Server{Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		once.Do(func() { close(entered) })
		<-release
		r := new(dns.Msg)
		r.SetReply(q)
		_ = w.WriteMsg(r)
	})}
	if protocol == dnscrypt.ProtoTCP {
		server.Listener = listener
		_ = packet.Close()
	} else {
		server.PacketConn = packet
		_ = listener.Close()
	}
	started := make(chan struct{})
	server.NotifyStartedFunc = func() { close(started) }
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ActivateAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.ShutdownContext(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-serveDone:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("certificate fixture did not stop")
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("certificate fixture did not start")
	}
	rc, err := dnscrypt.GenerateResolverConfig("held-cert.invalid", nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := rc.CreateStamp(address)
	if err != nil {
		t.Fatal(err)
	}
	return stamp.String()
}

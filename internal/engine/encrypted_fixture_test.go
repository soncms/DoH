//go:build !console

package engine

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/AdguardTeam/dnscrypt"
	"github.com/miekg/dns"
	appconfig "sondns/internal/config"
	"sondns/internal/testutil"
)

func TestLocalPinnedDNSCryptFixture(t *testing.T) {
	stamp, calls := testutil.StartLocalEncryptedDNSFixture(t)
	c := appconfig.Default()
	c.Upstreams = []appconfig.Upstream{{URL: stamp}}
	e, _, err := New(c, log.New(io.Discard, "", 0))
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
		testutil.RequireEncryptedFixtureReply(t, q, r)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("queries did not all reach pinned encrypted fixture: %d", got)
	}
	// Exercise the TCP listener explicitly as well as the engine's UDP path.
	client := dnscrypt.NewClient(&dnscrypt.ClientConfig{Proto: dnscrypt.ProtoTCP})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := client.DialContext(ctx, stamp)
	if err != nil {
		t.Fatal(err)
	}
	q := new(dns.Msg)
	q.SetQuestion("sondns-test.invalid.", dns.TypeA)
	r, err := client.ExchangeContext(ctx, q, info)
	if err != nil {
		t.Fatal(err)
	}
	testutil.RequireEncryptedFixtureReply(t, q, r)
	if got := calls.Load(); got != 5 {
		t.Fatalf("TCP query did not reach pinned fixture: %d", got)
	}
}

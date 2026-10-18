//go:build windows && !console

package service

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"sondns/internal/testutil"
)

type lifecycleIPv6Network struct {
	SchemaVersion                         int
	InterfaceIndex                        int
	Source, Destination                   string
	LinkLocalSource, LinkLocalDestination string
	SetupComplete, CleanupVerified        bool
}

type lifecycleIPv6Observation struct {
	Scope, QueryType, Local, Remote string
	Error                           string `json:",omitempty"`
	TimedOut, SyntheticReply        bool
	Elapsed                         time.Duration
}

type lifecycleIPv6Report struct {
	NetworkSetupSHA256         string
	Before, Intercepted, After []lifecycleIPv6Observation
}

type lifecycleIPv6Fixture struct {
	network lifecycleIPv6Network
	report  lifecycleIPv6Report
}

func prepareLifecycleIPv6(t *testing.T) *lifecycleIPv6Fixture {
	t.Helper()
	path := os.Getenv("SONDNS_TEST_IPV6_NETWORK")
	if !filepath.IsAbs(path) {
		t.Fatal("run the disposable Windows kit to prepare its IPv6 network fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &lifecycleIPv6Fixture{}
	if err := json.Unmarshal(data, &f.network); err != nil {
		t.Fatal(err)
	}
	if f.network.SchemaVersion != 1 || !f.network.SetupComplete || f.network.CleanupVerified || f.network.InterfaceIndex < 1 {
		t.Fatal("IPv6 fixture setup is incomplete")
	}
	for _, address := range []string{f.network.Source, f.network.Destination} {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() != nil || !strings.HasPrefix(ip.String(), "2001:db8:5344:") {
			t.Fatal("IPv6 fixture must use the documentation prefix")
		}
	}
	for _, address := range []string{f.network.LinkLocalSource, f.network.LinkLocalDestination} {
		ip := net.ParseIP(address)
		if ip == nil || ip.To4() != nil || !ip.IsLinkLocalUnicast() {
			t.Fatal("IPv6 fixture must include scoped link-local addresses")
		}
	}
	f.report.NetworkSetupSHA256 = migrationHash(data)
	t.Cleanup(func() {
		data, err := json.MarshalIndent(f.report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(filepath.Dir(path), "ipv6-dns.json"), append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Errorf("save IPv6 DNS report: %v", err)
		}
	})
	f.report.Before = f.probe(t, false)
	t.Log("IPv6 fixture before service: documentation and scoped link-local A/AAAA timed out")
	return f
}

func (f *lifecycleIPv6Fixture) probe(t *testing.T, running bool) []lifecycleIPv6Observation {
	t.Helper()
	var result []lifecycleIPv6Observation
	for _, scope := range []struct{ name, source, destination, zone string }{
		{"documentation", f.network.Source, f.network.Destination, ""},
		{"link-local", f.network.LinkLocalSource, f.network.LinkLocalDestination, strconv.Itoa(f.network.InterfaceIndex)},
	} {
		for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			observation := lifecycleIPv6Observation{Scope: scope.name, QueryType: dns.TypeToString[qtype]}
			client := &dns.Client{Net: "udp6", Timeout: 400 * time.Millisecond,
				Dialer: &net.Dialer{Timeout: 2 * time.Second, LocalAddr: &net.UDPAddr{IP: net.ParseIP(scope.source), Zone: scope.zone}}}
			if running {
				client.Timeout = 5 * time.Second
			}
			conn, err := client.Dial(net.JoinHostPort(scope.destination+zoneSuffix(scope.zone), "53"))
			if err != nil {
				t.Fatalf("IPv6 fixture cannot create a routed %s query: %v", scope.name, err)
			}
			// Validate both endpoints before using this socket. In particular a
			// link-local query must retain the intended interface scope.
			local, localOK := conn.LocalAddr().(*net.UDPAddr)
			remote, remoteOK := conn.RemoteAddr().(*net.UDPAddr)
			if !localOK || !remoteOK || !local.IP.Equal(net.ParseIP(scope.source)) || local.Port == 0 ||
				!remote.IP.Equal(net.ParseIP(scope.destination)) || remote.Port != 53 {
				_ = conn.Close()
				t.Fatalf("IPv6 socket endpoints differ: local=%v remote=%v", local, remote)
			}
			if scope.zone != "" {
				iface, err := net.InterfaceByIndex(f.network.InterfaceIndex)
				if err != nil || (remote.Zone != scope.zone && remote.Zone != iface.Name) {
					_ = conn.Close()
					t.Fatalf("IPv6 link-local scope lost: %v %v", remote, err)
				}
			}
			observation.Local, observation.Remote = local.String(), remote.String()
			query := new(dns.Msg)
			query.SetQuestion("sondns-test.invalid.", qtype)
			start := time.Now()
			reply, _, exchangeErr := client.ExchangeWithConn(query, conn)
			observation.Elapsed = time.Since(start)
			_ = conn.Close()
			if exchangeErr != nil {
				observation.Error = exchangeErr.Error()
			}
			if !running {
				var netErr net.Error
				observation.TimedOut = errors.As(exchangeErr, &netErr) && netErr.Timeout()
				if !observation.TimedOut || reply != nil {
					t.Fatalf("IPv6 %s must time out without interception, not fail routing or receive DNS: %v %v", scope.name, reply, exchangeErr)
				}
			} else {
				if exchangeErr != nil {
					t.Fatalf("transparent IPv6 %s %s: %v", scope.name, dns.TypeToString[qtype], exchangeErr)
				}
				testutil.RequireEncryptedFixtureReply(t, query, reply)
				if len(reply.Question) != 1 || reply.Question[0] != query.Question[0] || reply.Answer[0].Header().Rrtype != qtype {
					t.Fatal("transparent IPv6 changed the DNS question or response type")
				}
				observation.SyntheticReply = true
			}
			result = append(result, observation)
		}
	}
	return result
}

func zoneSuffix(zone string) string {
	if zone == "" {
		return ""
	}
	return "%" + zone
}

//go:build windows && !console

package gui

import (
	"testing"

	appconfig "sondns/internal/config"
	"sondns/internal/requestlog"
)

func TestCompactLogLatencyColumn(t *testing.T) {
	for _, tc := range []struct {
		us   int64
		want string
	}{{250, "250 µs"}, {12500, "12.5 ms"}, {0, "0.0 ms"}} {
		m := &requestLogModel{rows: []requestlog.Entry{{Upstream: "hidden-upstream", DurationUS: tc.us}}}
		if got := m.Value(0, 4); got != tc.want {
			t.Fatalf("latency = %v, want %s", got, tc.want)
		}
	}
}

func TestSonCMSEndpoint(t *testing.T) {
	const want = "https://dns.soncms.net/dns-query/sondns"
	if got := appconfig.PredefinedUpstream(appconfig.ProviderSonCMS).URL; got != want {
		t.Fatalf("preset URL = %q, want %q", got, want)
	}
	if got := appconfig.Default().Upstreams[0].URL; got != want {
		t.Fatalf("default URL = %q, want %q", got, want)
	}
}

//go:build !console

package engine

import (
	"context"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

// The policy belongs to one upstream generation. The proxy has already captured
// the original client's flags before Exchange, so its response filtering keeps
// working even though we request DNSSEC records with the cache disabled.
type dnssecUpstream struct {
	upstream.Upstream
	enabled bool
}

func (u *dnssecUpstream) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if !u.enabled {
		return u.Upstream.Exchange(ctx, req)
	}
	query := req.Copy()
	if opt := query.IsEdns0(); opt != nil {
		opt.SetDo()
	} else {
		query.SetEdns0(1232, true)
	}
	return u.Upstream.Exchange(ctx, query)
}

func wrapDNSSECUpstreams(conf *dnsproxy.UpstreamConfig, enabled bool) {
	for i, u := range conf.Upstreams {
		conf.Upstreams[i] = &dnssecUpstream{Upstream: u, enabled: enabled}
	}
}

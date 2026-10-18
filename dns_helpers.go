package main

import (
	"context"
	"errors"
	"github.com/ameshkov/dnsstamps"
	"github.com/miekg/dns"
	"net"
	"strings"
)

func responseCode(response *dns.Msg) string {
	if response == nil {
		return ""
	}
	return dns.RcodeToString[response.Rcode]
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errors.Is(err, context.DeadlineExceeded)
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func answerIPs(msg *dns.Msg) []string {
	if msg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, rr := range msg.Answer {
		var ip net.IP
		switch v := rr.(type) {
		case *dns.A:
			ip = v.A
		case *dns.AAAA:
			ip = v.AAAA
		}
		if ip == nil {
			continue
		}
		value := ip.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func upstreamBootstrap(item Upstream) (string, []string, error) {
	if strings.HasPrefix(strings.ToLower(item.URL), "sdns://") {
		stamp, err := dnsstamps.NewServerStampFromString(item.URL)
		if err != nil {
			return "", nil, err
		}
		addresses := append([]string(nil), item.Bootstrap...)
		if len(addresses) == 0 && stamp.ServerAddrStr != "" {
			addresses = append(addresses, stamp.ServerAddrStr)
		}
		return stamp.ProviderName, addresses, nil
	}
	return hostnameFromURL(item.URL), append([]string(nil), item.Bootstrap...), nil
}

func hostnameFromURL(raw string) string {
	start := strings.Index(raw, "://")
	if start < 0 {
		return raw
	}
	rest := raw[start+3:]
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		rest = rest[:end]
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[at+1:]
	}
	if host, _, err := net.SplitHostPort(rest); err == nil {
		return host
	}
	return strings.Trim(rest, "[]")
}

func anyDNSSEC(c Config) bool {
	for _, u := range c.Upstreams {
		if u.DNSSEC {
			return true
		}
	}
	return false
}

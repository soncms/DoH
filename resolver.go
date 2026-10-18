package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

type Resolver struct {
	engine interface {
		ResolveMsg(context.Context, *dns.Msg) (*dns.Msg, error)
	}
	requests *DNSRequestRing
}

func (r *Resolver) recordInterceptIssue(domain, qtype, result string, err error) {
	if r == nil || r.requests == nil {
		return
	}
	domain = strings.TrimSuffix(domain, ".")
	entry := DNSRequestEntry{Domain: domain, Type: qtype, Result: result}
	if err != nil {
		entry.Error = err.Error()
	}
	r.requests.Add(entry)
}

func (r *Resolver) Resolve(ctx context.Context, wire []byte) ([]byte, error) {
	if r == nil || r.engine == nil {
		return nil, errors.New("resolver engine not initialized")
	}
	request := new(dns.Msg)
	if err := request.Unpack(wire); err != nil {
		return nil, fmt.Errorf("invalid DNS query: %w", err)
	}
	response, err := r.engine.ResolveMsg(ctx, request)
	if response == nil {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("empty DNS response")
	}
	// The engine also returns a valid SERVFAIL on transport failure. Deliver
	// it to the client instead of silently dropping the intercepted query.
	return response.Pack()
}

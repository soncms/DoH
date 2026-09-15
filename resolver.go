package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var errAllUpstreams = errors.New("all DoH upstreams failed")

type Resolver struct {
	engine    *DNSProxyEngine
	clients   []*dohClient
	timeout   time.Duration
	mu        sync.RWMutex
	next      int
	local     *dns.Client
	localAddr string
	requests  *DNSRequestRing
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

type dohClient struct {
	endpoint  *url.URL
	http      *http.Client
	bootstrap []string
	dnssec    bool
}

func NewResolver(c Config) (*Resolver, error) {
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	r := &Resolver{timeout: c.Timeout}
	for _, upstream := range c.Upstreams {
		u, err := url.Parse(upstream.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, fmt.Errorf("upstream URL must be a valid https endpoint: %q", upstream.URL)
		}
		if u.Path == "" {
			u.Path = "/dns-query"
		}
		port := u.Port()
		if port == "" {
			port = "443"
		}
		bootstrap := append([]string(nil), upstream.Bootstrap...)
		if len(bootstrap) == 0 {
			bootstrap, err = resolveEndpointBootstrap(u, c.Timeout)
			if err != nil {
				return nil, err
			}
		}
		transport := &http.Transport{
			// DoH is intentionally sent directly to the bootstrap IP. This avoids
			// proxy/DNS recursion when a VPN owns the machine's DNS settings.
			Proxy:                 nil,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   c.Timeout,
			ResponseHeaderTimeout: c.Timeout,
			DialContext:           bootstrapDialer(u.Hostname(), port, bootstrap, c.Timeout),
		}
		r.clients = append(r.clients, &dohClient{endpoint: u, http: &http.Client{Transport: transport, Timeout: c.Timeout}, bootstrap: bootstrap, dnssec: upstream.DNSSEC})
	}
	if len(r.clients) == 0 {
		return nil, errors.New("no DoH upstreams configured")
	}
	return r, nil
}

// Resolve the endpoint before interception starts, so later DoH connections
// never need to query the same DNS proxy to find their own upstream hostname.
func resolveEndpointBootstrap(endpoint *url.URL, timeout time.Duration) ([]string, error) {
	host := endpoint.Hostname()
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	if ip := net.ParseIP(host); ip != nil {
		return []string{net.JoinHostPort(ip.String(), port)}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve DoH hostname %q before starting DNS interception: %w", host, err)
	}
	var ipv4, ipv6 []string
	for _, address := range addresses {
		value := net.JoinHostPort(address.IP.String(), port)
		if address.IP.To4() != nil {
			ipv4 = append(ipv4, value)
		} else {
			ipv6 = append(ipv6, value)
		}
	}
	bootstrap := append(ipv4, ipv6...)
	if len(bootstrap) == 0 {
		return nil, fmt.Errorf("DoH hostname %q has no IP addresses", host)
	}
	return bootstrap, nil
}

func bootstrapDialer(host, port string, addresses []string, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var last error
		if len(addresses) == 0 {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(host, port))
		}
		for _, addr := range addresses {
			d := net.Dialer{Timeout: timeout}
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				last = err
				continue
			}
			// TLS verifies the configured DoH hostname even though the TCP dial
			// goes to a literal bootstrap address.
			return conn, nil
		}
		return nil, last
	}
}

func (r *Resolver) Resolve(ctx context.Context, wire []byte) ([]byte, error) {
	if r.engine != nil {
		request := new(dns.Msg)
		if err := request.Unpack(wire); err != nil {
			return nil, fmt.Errorf("invalid DNS query: %w", err)
		}
		response, err := r.engine.ResolveMsg(ctx, request)
		if err != nil && response == nil {
			return nil, err
		}
		if response == nil {
			return nil, errors.New("empty DNS response")
		}
		return response.Pack()
	}
	if r.local != nil {
		request := new(dns.Msg)
		if err := request.Unpack(wire); err != nil {
			return nil, fmt.Errorf("invalid DNS query: %w", err)
		}
		response, _, err := r.local.ExchangeContext(ctx, request, r.localAddr)
		if err != nil {
			return nil, err
		}
		if response == nil {
			return nil, errors.New("local resolver returned an empty response")
		}
		if response.Truncated {
			tcp := &dns.Client{Net: "tcp", Timeout: r.timeout}
			response, _, err = tcp.ExchangeContext(ctx, request, r.localAddr)
			if err != nil {
				return nil, err
			}
		}
		return response.Pack()
	}
	r.mu.Lock()
	start := r.next
	r.next = (r.next + 1) % len(r.clients)
	r.mu.Unlock()
	var last error
	for i := 0; i < len(r.clients); i++ {
		client := r.clients[(start+i)%len(r.clients)]
		response, err := client.resolve(ctx, wire)
		if err == nil {
			return response, nil
		}
		last = err
	}
	if last == nil {
		last = errAllUpstreams
	}
	return nil, fmt.Errorf("%w: %v", errAllUpstreams, last)
}

func (d *dohClient) resolve(ctx context.Context, wire []byte) ([]byte, error) {
	request := new(dns.Msg)
	if err := request.Unpack(wire); err != nil {
		return nil, fmt.Errorf("invalid DNS query: %w", err)
	}
	if d.dnssec {
		opt := request.IsEdns0()
		if opt == nil {
			request.SetEdns0(1232, true)
		} else {
			opt.SetDo()
		}
		var err error
		wire, err = request.Pack()
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint.String(), bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned HTTP %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		mediaType, _, parseErr := mime.ParseMediaType(ct)
		if parseErr != nil || !strings.EqualFold(mediaType, "application/dns-message") {
			return nil, fmt.Errorf("upstream returned unsupported content type %q", ct)
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, errors.New("upstream returned a short DNS message")
	}
	message := new(dns.Msg)
	if err := message.Unpack(data); err != nil {
		return nil, fmt.Errorf("upstream returned invalid DNS message: %w", err)
	}
	if !message.Response {
		return nil, errors.New("upstream returned a DNS query instead of a response")
	}
	if len(message.Question) != len(request.Question) {
		return nil, errors.New("upstream response question section does not match query")
	}
	for i := range request.Question {
		want, got := request.Question[i], message.Question[i]
		if !strings.EqualFold(want.Name, got.Name) || want.Qtype != got.Qtype || want.Qclass != got.Qclass {
			return nil, errors.New("upstream response question does not match query")
		}
	}
	return data, nil
}

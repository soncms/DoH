package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	proxyupstream "github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/ameshkov/dnsstamps"
	"github.com/miekg/dns"
)

type DNSProxyEngine struct {
	logger       *log.Logger
	proxy        *dnsproxy.Proxy
	requests     *DNSRequestRing
	upstreams    *dnsproxy.UpstreamConfig
	ipc          *dnsIPCServer
	timeout      time.Duration
	bootstrapDNS []string
	mu           sync.RWMutex
	started      bool
}

func prepareUpstreamsConfig(items []Upstream, bootstrapDNS []string, timeout time.Duration, logger *log.Logger) (*dnsproxy.UpstreamConfig, error) {
	lines := make([]string, 0, len(items))
	bootstrap := bootstrapMap{}
	for _, item := range items {
		lines = append(lines, item.URL)
		host := strings.ToLower(strings.TrimSuffix(hostnameFromURL(item.URL), "."))
		addresses := append([]string(nil), item.Bootstrap...)
		if strings.HasPrefix(item.URL, "sdns://") {
			stamp, stampErr := dnsstamps.NewServerStampFromString(item.URL)
			if stampErr != nil {
				return nil, stampErr
			}
			host = strings.ToLower(strings.TrimSuffix(stamp.ProviderName, "."))
			if len(addresses) == 0 && stamp.ServerAddrStr != "" {
				addresses = append(addresses, stamp.ServerAddrStr)
			}
		}
		for _, value := range addresses {
			addr, parseErr := netip.ParseAddrPort(value)
			ip := addr.Addr()
			if parseErr != nil {
				ip, parseErr = netip.ParseAddr(value)
				if parseErr != nil {
					return nil, fmt.Errorf("invalid bootstrap IP %q: %w", value, parseErr)
				}
			}
			bootstrap[host] = append(bootstrap[host], ip)
		}
		if len(bootstrap[host]) == 0 && host != "" {
			if hardcoded := bootstrapForHost(host); len(hardcoded) > 0 {
				for _, h := range hardcoded {
					if addr, parseErr := netip.ParseAddrPort(h); parseErr == nil {
						bootstrap[host] = append(bootstrap[host], addr.Addr())
					}
				}
			}
		}
		if len(bootstrap[host]) == 0 && host != "" {
			ips, lookupErr := resolveHostViaBootstrapDNS(bootstrapDNS, host, timeout, logger)
			if lookupErr != nil || len(ips) == 0 {
				return nil, fmt.Errorf("bootstrap %s: %w", host, lookupErr)
			}
			bootstrap[host] = ips
		}
	}
	logWriter := slogWriter{logger: logger}
	upstreamTimeout := timeout
	if upstreamTimeout <= 0 || upstreamTimeout > 3*time.Second {
		upstreamTimeout = 3 * time.Second
	}
	options := &proxyupstream.Options{Logger: slog.New(slog.NewTextHandler(logWriter, nil)), Timeout: upstreamTimeout, Bootstrap: bootstrap}
	return dnsproxy.ParseUpstreamsConfig(lines, options)
}

func (e *DNSProxyEngine) ClearCache() {
	if e != nil && e.proxy != nil {
		e.proxy.ClearCache()
	}
}

func (e *DNSProxyEngine) SetUpstreams(items []Upstream) error {
	if e == nil || e.proxy == nil || e.upstreams == nil {
		return errors.New("engine not initialized")
	}
	if len(items) == 0 {
		return errors.New("no upstreams provided")
	}
	timeout := e.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	newConf, err := prepareUpstreamsConfig(items, e.bootstrapDNS, timeout, e.logger)
	if err != nil {
		return err
	}
	e.mu.Lock()
	oldUpstreams := e.upstreams.Upstreams
	e.upstreams.Upstreams = newConf.Upstreams
	e.upstreams.DomainReservedUpstreams = newConf.DomainReservedUpstreams
	e.upstreams.SpecifiedDomainUpstreams = newConf.SpecifiedDomainUpstreams
	e.upstreams.SubdomainExclusions = newConf.SubdomainExclusions
	e.mu.Unlock()

	e.proxy.ClearCache()

	go func() {
		time.Sleep(1 * time.Second)
		for _, u := range oldUpstreams {
			_ = u.Close()
		}
	}()
	return nil
}

func NewDNSProxyEngine(c Config, logger *log.Logger) (*DNSProxyEngine, *Resolver, error) {
	if err := c.normalize(); err != nil {
		return nil, nil, err
	}
	if logger == nil {
		logger = log.Default()
	}
	udpAddr, err := net.ResolveUDPAddr("udp", c.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve UDP listen address: %w", err)
	}
	tcpAddr, err := net.ResolveTCPAddr("tcp", c.Listen)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve TCP listen address: %w", err)
	}
	upstreams, err := prepareUpstreamsConfig(c.Upstreams, c.BootstrapDNS, c.Timeout, logger)
	if err != nil {
		if upstreams != nil {
			_ = upstreams.Close()
		}
		return nil, nil, fmt.Errorf("parse upstreams: %w", err)
	}
	logWriter := slogWriter{logger: logger}
	ring := NewDNSRequestRing()
	engineProxy, err := dnsproxy.New(&dnsproxy.Config{
		Logger:          slog.New(slog.NewTextHandler(logWriter, nil)),
		UpstreamConfig:  upstreams,
		UpstreamMode:    dnsproxy.UpstreamModeParallel,
		UDPListenAddr:   []*net.UDPAddr{udpAddr},
		TCPListenAddr:   []*net.TCPAddr{tcpAddr},
		CacheEnabled:    c.CacheEnabled,
		CacheSizeBytes:  c.CacheSize,
		CacheMinTTL:     60,
		CacheMaxTTL:     86400,
		CacheOptimistic: true,
		DNSSECEnabled:   anyDNSSEC(c),
		MaxGoroutines:   2048,
		UDPBufferSize:   1 << 20,
		PendingRequests: &dnsproxy.PendingRequestsConfig{Enabled: true},
		RequestHandler:  requestLogHandler{ring: ring},
	})
	if err != nil {
		_ = upstreams.Close()
		return nil, nil, fmt.Errorf("create DNS proxy: %w", err)
	}
	engine := &DNSProxyEngine{
		logger:       logger,
		proxy:        engineProxy,
		requests:     ring,
		upstreams:    upstreams,
		timeout:      c.Timeout,
		bootstrapDNS: c.BootstrapDNS,
	}
	return engine, &Resolver{engine: engine, local: &dns.Client{Net: "udp", Timeout: c.Timeout}, localAddr: c.Listen, timeout: c.Timeout, requests: ring}, nil
}

func (e *DNSProxyEngine) ResolveMsg(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if e == nil || e.proxy == nil {
		return nil, errors.New("engine not initialized")
	}
	const maxAttempts = 2
	var lastErr error
	perAttemptTimeout := 2 * time.Second
	if e.timeout > 0 && e.timeout < perAttemptTimeout {
		perAttemptTimeout = e.timeout
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Copy the request because dnsproxy mutates the message in place
		// (EDNS0 flags, ID handling) and the same query is reused on retry.
		dctx := &dnsproxy.DNSContext{
			Req:   req.Copy(),
			Proto: dnsproxy.ProtoUDP,
		}
		handler := requestLogHandler{ring: e.requests}
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		lastErr = handler.ServeDNS(attemptCtx, e.proxy, dctx)
		cancel()
		// ServeDNS always fills dctx.Res, even with a synthetic SERVFAIL when
		// every upstream fails, so a non-nil response does not mean success.
		// Retry only when the upstream exchange itself returned an error.
		if lastErr == nil {
			return dctx.Res, nil
		}
	}
	servfail := new(dns.Msg)
	servfail.SetReply(req)
	servfail.Rcode = dns.RcodeServerFailure
	servfail.RecursionAvailable = true
	return servfail, lastErr
}

type requestLogHandler struct {
	ring *DNSRequestRing
}

func (h requestLogHandler) ServeDNS(ctx context.Context, p *dnsproxy.Proxy, dctx *dnsproxy.DNSContext) (err error) {
	started := time.Now()
	defer func() { h.record(dctx, err, time.Since(started)) }()
	err = (dnsproxy.DefaultHandler{}).ServeDNS(ctx, p, dctx)
	return err
}

func (h requestLogHandler) record(dctx *dnsproxy.DNSContext, err error, elapsed time.Duration) {
	if h.ring == nil || dctx == nil || dctx.Req == nil || len(dctx.Req.Question) == 0 {
		return
	}
	question := dctx.Req.Question[0]
	result := requestResult(err, dctx.Res, dctx.QueryStatistics())
	qtype := dns.TypeToString[question.Qtype]
	if qtype == "" {
		qtype = fmt.Sprintf("TYPE%d", question.Qtype)
	}
	domain := strings.TrimSuffix(question.Name, ".")
	entry := DNSRequestEntry{Domain: domain, Type: qtype, Result: result, LatencyMS: elapsed.Milliseconds(), AnswerIPs: answerIPs(dctx.Res)}
	if err != nil {
		entry.Error = err.Error()
	}
	if entry.Error == "" {
		entry.Error = statsErrorSummary(dctx.QueryStatistics())
	}
	h.ring.Add(entry)
}

func requestResult(err error, response *dns.Msg, stats *dnsproxy.QueryStatistics) string {
	if isTimeoutError(err) || err != nil && statsContainTimeout(stats) {
		return "TIMEOUT"
	}
	if response != nil {
		if name := dns.RcodeToString[response.Rcode]; name != "" {
			if err != nil && response.Rcode == dns.RcodeServerFailure {
				return "ERROR"
			}
			return name
		}
	}
	if err != nil {
		return "ERROR"
	}
	return "ERROR"
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

func statsContainTimeout(stats *dnsproxy.QueryStatistics) bool {
	if stats == nil {
		return false
	}
	for _, group := range [][]*dnsproxy.UpstreamStatistics{stats.Main(), stats.Fallback()} {
		for _, item := range group {
			if item == nil || item.Error == nil {
				continue
			}
			if isTimeoutError(item.Error) {
				return true
			}
		}
	}
	return false
}

func statsErrorSummary(stats *dnsproxy.QueryStatistics) string {
	if stats == nil {
		return ""
	}
	var messages []string
	for _, group := range [][]*dnsproxy.UpstreamStatistics{stats.Main(), stats.Fallback()} {
		for _, item := range group {
			if item != nil && item.Error != nil {
				messages = append(messages, item.Error.Error())
			}
		}
	}
	return strings.Join(messages, "; ")
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

func (e *DNSProxyEngine) Start(ctx context.Context) error {
	if err := e.proxy.Start(ctx); err != nil {
		_ = e.upstreams.Close()
		return err
	}
	e.started = true
	e.ipc = newDNSIPCServer(e.requests, e)
	e.ipc.start()
	return nil
}

func (e *DNSProxyEngine) Shutdown(ctx context.Context) error {
	if e == nil || e.proxy == nil {
		return nil
	}
	if e.ipc != nil {
		e.ipc.close()
	}
	return e.proxy.Shutdown(ctx)
}

type slogWriter struct{ logger *log.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	if w.logger != nil {
		w.logger.Print(strings.TrimSpace(string(p)))
	}
	return len(p), nil
}

type bootstrapMap map[string][]netip.Addr

func (r bootstrapMap) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if addrs := r[strings.ToLower(strings.TrimSuffix(host, "."))]; len(addrs) > 0 {
		filtered := make([]netip.Addr, 0, len(addrs))
		for _, addr := range addrs {
			if network == "ip4" && !addr.Is4() || network == "ip6" && !addr.Is6() {
				continue
			}
			filtered = append(filtered, addr)
		}
		if len(filtered) > 0 {
			return filtered, nil
		}
		return nil, fmt.Errorf("no %s bootstrap IP prepared for %s", network, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	return nil, fmt.Errorf("no bootstrap IP prepared for %s", host)
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

var _ proxyupstream.Resolver = bootstrapMap(nil)

func resolveHostViaBootstrapDNS(servers []string, host string, timeout time.Duration, logger *log.Logger) ([]netip.Addr, error) {
	if len(servers) == 0 {
		servers = DefaultBootstrapDNS
	}
	fqdn := dns.Fqdn(host)
	clientTimeout := 1500 * time.Millisecond
	if timeout > 0 && timeout < clientTimeout {
		clientTimeout = timeout
	}

	type lookupResult struct {
		server string
		ips    []netip.Addr
		err    error
	}

	for attempt := 0; attempt < 3; attempt++ {
		resCh := make(chan lookupResult, len(servers))
		for _, srv := range servers {
			go func(server string) {
				ips, err := queryBootstrapServer(server, fqdn, clientTimeout)
				resCh <- lookupResult{server: server, ips: ips, err: err}
			}(srv)
		}

		var firstErr error
		for i := 0; i < len(servers); i++ {
			res := <-resCh
			if res.err == nil && len(res.ips) > 0 {
				if logger != nil {
					logger.Printf("bootstrap DNS: resolved %s via %s to %v", host, res.server, res.ips)
				}
				return res.ips, nil
			}
			if firstErr == nil && res.err != nil {
				firstErr = res.err
			}
		}
		time.Sleep(250 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, lookupErr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if lookupErr == nil && len(ips) > 0 {
		return ips, nil
	}
	return nil, fmt.Errorf("bootstrap lookup %s via %v failed: %w", host, servers, lookupErr)
}

func queryBootstrapServer(server, fqdn string, timeout time.Duration) ([]netip.Addr, error) {
	networks := []string{"udp", "tcp"}
	var found []netip.Addr
	var lastErr error

	for _, network := range networks {
		c := &dns.Client{Net: network, Timeout: timeout}
		for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			msg := new(dns.Msg)
			msg.SetQuestion(fqdn, qtype)
			msg.RecursionDesired = true
			resp, _, err := c.Exchange(msg, server)
			if err != nil {
				lastErr = err
				continue
			}
			if resp == nil || resp.Rcode != dns.RcodeSuccess {
				continue
			}
			for _, ans := range resp.Answer {
				switch r := ans.(type) {
				case *dns.A:
					if addr, ok := netip.AddrFromSlice(r.A); ok {
						found = append(found, addr.Unmap())
					}
				case *dns.AAAA:
					if addr, ok := netip.AddrFromSlice(r.AAAA); ok {
						found = append(found, addr.Unmap())
					}
				}
			}
		}
		if len(found) > 0 {
			return found, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no A or AAAA records found for %s", fqdn)
}

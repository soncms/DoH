//go:build !console

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
	"time"

	dnsproxy "github.com/AdguardTeam/dnsproxy/proxy"
	proxyupstream "github.com/AdguardTeam/dnsproxy/upstream"
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
	mu           upstreamMutex
	started      bool
}

func prepareUpstreamsConfig(ctx context.Context, items []Upstream, bootstrapDNS []string, timeout time.Duration, logger *log.Logger) (*dnsproxy.UpstreamConfig, error) {
	items, err := normalizeUpstreams(items)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(items))
	bootstrap := bootstrapMap{}
	for _, item := range items {
		lines = append(lines, item.URL)
		host, addresses, err := upstreamBootstrap(item)
		if err != nil {
			return nil, err
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
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
			ips, lookupErr := resolveHostViaBootstrapDNS(ctx, bootstrapDNS, host, timeout, logger)
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
	conf, err := dnsproxy.ParseUpstreamsConfig(lines, options)
	if err == nil {
		wrapDNSSECUpstreams(conf, anyDNSSEC(Config{Upstreams: items}))
	}
	return conf, err
}

func (e *DNSProxyEngine) SetUpstreams(ctx context.Context, items []Upstream) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
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
	newConf, err := prepareUpstreamsConfig(ctx, items, e.bootstrapDNS, timeout, e.logger)
	if err != nil {
		if newConf != nil {
			_ = newConf.Close()
		}
		return err
	}
	if err = e.replaceUpstreamsContext(ctx, newConf); err != nil {
		_ = newConf.Close()
		return err
	}
	return nil
}

func (e *DNSProxyEngine) replaceUpstreams(newConf *dnsproxy.UpstreamConfig) {
	_ = e.replaceUpstreamsContext(context.Background(), newConf)
}

func (e *DNSProxyEngine) replaceUpstreamsContext(ctx context.Context, newConf *dnsproxy.UpstreamConfig) error {
	if err := e.mu.LockContext(ctx); err != nil {
		return err
	}
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Request handlers share this lock: a swap waits for active queries before
	// replacing the configuration or closing their transports.
	oldConf := *e.upstreams
	*e.upstreams = *newConf
	_ = oldConf.Close()
	return nil
}

func NewDNSProxyEngine(c Config, logger *log.Logger) (*DNSProxyEngine, *Resolver, error) {
	c = c.Clone()
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
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	upstreams, err := prepareUpstreamsConfig(ctx, c.Upstreams, c.BootstrapDNS, c.Timeout, logger)
	if err != nil {
		if upstreams != nil {
			_ = upstreams.Close()
		}
		return nil, nil, fmt.Errorf("parse upstreams: %w", err)
	}
	logWriter := slogWriter{logger: logger}
	ring := NewDNSRequestRing()
	engine := &DNSProxyEngine{
		logger: logger, requests: ring, upstreams: upstreams,
		timeout: c.Timeout, bootstrapDNS: c.BootstrapDNS,
	}
	engineProxy, err := dnsproxy.New(&dnsproxy.Config{
		Logger:         slog.New(slog.NewTextHandler(logWriter, nil)),
		UpstreamConfig: upstreams,
		UpstreamMode:   dnsproxy.UpstreamModeParallel,
		UDPListenAddr:  []*net.UDPAddr{udpAddr},
		TCPListenAddr:  []*net.TCPAddr{tcpAddr},
		// Every client request must reach upstream, including repeated queries.
		CacheEnabled:    false,
		DNSSECEnabled:   false, // Per-generation upstream adapter works with cache disabled.
		MaxGoroutines:   2048,
		UDPBufferSize:   1 << 20,
		PendingRequests: &dnsproxy.PendingRequestsConfig{Enabled: false},
		RequestHandler:  requestLogHandler{ring: ring, configMu: &engine.mu},
	})
	if err != nil {
		_ = upstreams.Close()
		return nil, nil, fmt.Errorf("create DNS proxy: %w", err)
	}
	engine.proxy = engineProxy
	return engine, &Resolver{engine: engine, requests: ring}, nil
}

func (e *DNSProxyEngine) ResolveMsg(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if e == nil || e.proxy == nil {
		return nil, errors.New("engine not initialized")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	started := time.Now()
	// Keep the original query available so cancellation before the first
	// attempt still produces exactly one finalized client-request row.
	lastContext := &dnsproxy.DNSContext{Req: req.Copy(), Proto: dnsproxy.ProtoUDP}
	const maxAttempts = 2
	var lastErr error
	attempts := make([]DNSAttempt, 0, maxAttempts)
	defer func() {
		(requestLogHandler{ring: e.requests}).recordWithAttempts(lastContext, lastErr, time.Since(started), attempts)
	}()
	perAttemptTimeout := 2 * time.Second
	if e.timeout > 0 && e.timeout < perAttemptTimeout {
		perAttemptTimeout = e.timeout
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		// Copy the request because dnsproxy mutates the message in place
		// (EDNS0 flags, ID handling) and the same query is reused on retry.
		dctx := &dnsproxy.DNSContext{
			Req:   req.Copy(),
			Proto: dnsproxy.ProtoUDP,
		}
		lastContext = dctx
		handler := requestLogHandler{}
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		attemptStarted := time.Now()
		lastErr = handler.ServeDNS(attemptCtx, e.proxy, dctx)
		cancel()
		attempts = append(attempts, attemptFromContext(len(attempts)+1, dctx, lastErr, time.Since(attemptStarted)))
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
	ring     *DNSRequestRing
	configMu interface {
		RLock()
		RUnlock()
	}
}

func (h requestLogHandler) ServeDNS(ctx context.Context, p *dnsproxy.Proxy, dctx *dnsproxy.DNSContext) (err error) {
	if h.configMu != nil {
		h.configMu.RLock()
		defer h.configMu.RUnlock()
	}
	started := time.Now()
	defer func() { h.record(dctx, err, time.Since(started)) }()
	err = (dnsproxy.DefaultHandler{}).ServeDNS(ctx, p, dctx)
	return err
}

func (h requestLogHandler) record(dctx *dnsproxy.DNSContext, err error, elapsed time.Duration) {
	h.recordWithAttempts(dctx, err, elapsed, nil)
}

func (h requestLogHandler) recordWithAttempts(dctx *dnsproxy.DNSContext, err error, elapsed time.Duration, attempts []DNSAttempt) {
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
	stats := dctx.QueryStatistics()
	completed := time.Now()
	entry := DNSRequestEntry{
		Time: completed.Add(-elapsed), CompletedTime: completed,
		Domain: domain, Type: qtype, Protocol: protocolName(dctx.Proto),
		Result: result, RCode: responseCode(dctx.Res),
		LatencyMS: elapsed.Milliseconds(), DurationUS: elapsed.Microseconds(),
		AnswerIPs: answerIPs(dctx.Res), Attempts: append([]DNSAttempt(nil), attempts...),
	}
	entry.AttemptCount = len(entry.Attempts)
	if entry.AttemptCount == 0 && stats != nil {
		entry.AttemptCount = len(stats.Main()) + len(stats.Fallback())
	}
	if stats != nil {
		for _, item := range append(stats.Main(), stats.Fallback()...) {
			if item == nil {
				continue
			}
			if item.Address != "" && entry.Upstream == "" {
				entry.Upstream = item.Address
			}
		}
	}
	if len(entry.Attempts) == 0 {
		entry.Attempts = []DNSAttempt{attemptFromStats(1, stats, err, elapsed)}
		entry.AttemptCount = 1
	}
	if err != nil {
		entry.Error = err.Error()
	}
	if entry.Error == "" {
		entry.Error = statsErrorSummary(stats)
	}
	h.ring.Add(entry)
}

func attemptFromContext(number int, dctx *dnsproxy.DNSContext, err error, elapsed time.Duration) DNSAttempt {
	attempt := DNSAttempt{Number: number, DurationUS: elapsed.Microseconds()}
	if dctx == nil {
		if err != nil {
			attempt.Error = err.Error()
		}
		return attempt
	}
	stats := dctx.QueryStatistics()
	if stats != nil {
		for _, item := range append(stats.Main(), stats.Fallback()...) {
			if item != nil && item.Address != "" {
				attempt.Upstreams = append(attempt.Upstreams, item.Address)
			}
			if item != nil && item.Error != nil && attempt.Error == "" {
				attempt.Error = item.Error.Error()
			}
		}
	}
	if err != nil {
		attempt.Error = err.Error()
	}
	return attempt
}

func attemptFromStats(number int, stats *dnsproxy.QueryStatistics, err error, elapsed time.Duration) DNSAttempt {
	attempt := DNSAttempt{Number: number, DurationUS: elapsed.Microseconds()}
	if stats != nil {
		for _, item := range append(stats.Main(), stats.Fallback()...) {
			if item == nil {
				continue
			}
			if item.Address != "" {
				attempt.Upstreams = append(attempt.Upstreams, item.Address)
			}
			if item.Error != nil && attempt.Error == "" {
				attempt.Error = item.Error.Error()
			}
		}
	}
	if err != nil {
		attempt.Error = err.Error()
	}
	return attempt
}

func protocolName(proto any) string {
	return fmt.Sprint(proto)
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

func (e *DNSProxyEngine) Start(ctx context.Context) error {
	if err := e.proxy.Start(ctx); err != nil {
		_ = e.upstreams.Close()
		return err
	}
	e.started = true
	e.ipc = newDNSIPCServer(e.requests, e)
	if err := e.ipc.start(); err != nil {
		_ = e.proxy.Shutdown(ctx)
		e.started = false
		return fmt.Errorf("start DNS control pipe: %w", err)
	}
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

var _ proxyupstream.Resolver = bootstrapMap(nil)

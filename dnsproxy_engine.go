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
	config       Config
	configPath   string
	dpi          *DPIBackend
	startedAt    time.Time
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
		if newConf != nil {
			_ = newConf.Close()
		}
		return err
	}
	e.replaceUpstreams(newConf)
	e.mu.Lock()
	e.config.Upstreams = append([]Upstream(nil), items...)
	e.mu.Unlock()
	return nil
}

func (e *DNSProxyEngine) setConfigPath(path string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.configPath = path
	e.mu.Unlock()
}

func (e *DNSProxyEngine) configSnapshot() Config {
	if e == nil {
		return DefaultConfig()
	}
	e.mu.RLock()
	c := e.config
	c.Upstreams = append([]Upstream(nil), e.config.Upstreams...)
	c.BootstrapDNS = append([]string(nil), e.config.BootstrapDNS...)
	c.DPI.Targets = append([]string(nil), e.config.DPI.Targets...)
	c.DPI.Exclude = append([]string(nil), e.config.DPI.Exclude...)
	c.DPI.Arguments = append([]string(nil), e.config.DPI.Arguments...)
	e.mu.RUnlock()
	return c
}

func (e *DNSProxyEngine) setConfig(c Config) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.config = c
	e.mu.Unlock()
}

func (e *DNSProxyEngine) setDPIBackend(backend *DPIBackend) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.dpi = backend
	e.mu.Unlock()
}

func (e *DNSProxyEngine) persistConfig() error {
	if e == nil {
		return errors.New("engine not initialized")
	}
	e.mu.RLock()
	path := e.configPath
	c := e.config
	e.mu.RUnlock()
	if path == "" {
		return nil
	}
	return SaveConfig(path, c)
}

func (e *DNSProxyEngine) ipcState() *dnsIPCState {
	if e == nil {
		return &dnsIPCState{}
	}
	c := e.configSnapshot()
	e.mu.RLock()
	started, startedAt := e.started, e.startedAt
	e.mu.RUnlock()
	state := &dnsIPCState{
		DNSRunning:  started,
		DPIRunning:  c.DPI.Enabled,
		DPIEnabled:  c.DPI.Enabled,
		CaptureMode: c.DPI.CaptureMode,
		Provider:    c.CurrentProvider(),
	}
	e.mu.RLock()
	dpi := e.dpi
	e.mu.RUnlock()
	if dpi != nil {
		state.DPIRunning, state.LastError = dpi.Status()
	}
	assets := runtimeAssetDiagnostics()
	state.AssetVerified = len(assets) > 0
	for _, asset := range assets {
		if !asset.Verified {
			state.AssetVerified = false
			if state.AssetError == "" {
				state.AssetError = fmt.Sprintf("%s expected %s got %s", asset.Name, asset.Expected, asset.Actual)
				if asset.Error != "" {
					state.AssetError += ": " + asset.Error
				}
			}
		}
	}
	if !startedAt.IsZero() {
		state.UptimeSeconds = int64(time.Since(startedAt).Seconds())
	}
	return state
}

func (e *DNSProxyEngine) ipcConfig() *dnsIPCConfig {
	c := e.configSnapshot()
	return &dnsIPCConfig{
		Provider:         c.CurrentProvider(),
		Upstreams:        c.Upstreams,
		DPIEnabled:       c.DPI.Enabled,
		CaptureMode:      c.DPI.CaptureMode,
		ServiceAutoStart: c.ServiceAutoStart,
	}
}

func (e *DNSProxyEngine) replaceUpstreams(newConf *dnsproxy.UpstreamConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Request handlers share this lock: a swap waits for active queries before
	// replacing the configuration or closing their transports.
	oldConf := *e.upstreams
	*e.upstreams = *newConf
	_ = oldConf.Close()
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
	engine := &DNSProxyEngine{
		logger: logger, requests: ring, upstreams: upstreams,
		config:  c,
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
		DNSSECEnabled:   anyDNSSEC(c),
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
	return engine, &Resolver{engine: engine, local: &dns.Client{Net: "udp", Timeout: c.Timeout}, localAddr: c.Listen, timeout: c.Timeout, requests: ring}, nil
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
	configMu *sync.RWMutex
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

func responseCode(response *dns.Msg) string {
	if response == nil {
		return ""
	}
	return dns.RcodeToString[response.Rcode]
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
	e.mu.Lock()
	e.started = true
	e.startedAt = time.Now()
	e.mu.Unlock()
	e.ipc = newDNSIPCServer(e.requests, e)
	e.ipc.engine = e
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
	err := e.proxy.Shutdown(ctx)
	e.mu.Lock()
	e.started = false
	e.mu.Unlock()
	return err
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

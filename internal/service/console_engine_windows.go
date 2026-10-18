//go:build windows && console

package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ameshkov/dnsstamps"
	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	"sondns/internal/buildinfo"
	appconfig "sondns/internal/config"
	"sondns/internal/control"
	dnsengine "sondns/internal/engine"
	"sondns/internal/requestlog"
)

type managedChild struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startChild(exe string, args []string, logger *log.Logger) (*managedChild, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Stdout = logWriter{logger: logger, prefix: "dnsproxy: "}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	closeJob, err := attachHelperJob(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	child := &managedChild{cmd: cmd, done: make(chan struct{})}
	go func() { child.err = cmd.Wait(); closeJob(); close(child.done) }()
	return child, nil
}

func (c *managedChild) alive() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *managedChild) stop() {
	if c == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	<-c.done
}

type helperGeneration struct {
	child     *managedChild
	address   string
	dnssec    bool
	upstreams []appconfig.Upstream
}

type consoleEngine struct {
	cfg        appconfig.Config
	root       string
	lock       componentLock
	logger     *log.Logger
	ring       *requestlog.Ring
	mu         dnsengine.UpstreamMutex
	changes    sync.Mutex
	generation *helperGeneration
	ipc        *control.Server
	pipe       string
	udp, tcp   *dns.Server
	ctx        context.Context
	cancel     context.CancelFunc
	monitor    sync.WaitGroup
	closed     bool
	extraArgs  []string // Only populated by local transport tests.
}

func newConsoleEngine(c appconfig.Config, root string, lock componentLock, logger *log.Logger) *consoleEngine {
	if logger == nil {
		logger = log.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &consoleEngine{cfg: c.Clone(), root: root, lock: lock, logger: logger, ring: requestlog.New(), pipe: consolePipe(root, false), ctx: ctx, cancel: cancel}
}

func officialUpstreamArgs(ctx context.Context, c appconfig.Config, logger *log.Logger) ([]string, error) {
	items, err := appconfig.NormalizeUpstreams(c.Upstreams)
	if err != nil {
		return nil, err
	}
	var args []string
	for _, item := range items {
		var stamp dnsstamps.ServerStamp
		var host, port string
		if strings.HasPrefix(item.URL, "sdns://") {
			stamp, err = dnsstamps.NewServerStampFromString(item.URL)
			if err != nil {
				return nil, err
			}
			if stamp.ServerAddrStr != "" {
				args = append(args, "--upstream="+item.URL)
				continue
			}
			host = stamp.ProviderName
			switch stamp.Proto {
			case dnsstamps.StampProtoTypeDoH:
				port = "443"
			case dnsstamps.StampProtoTypeTLS, dnsstamps.StampProtoTypeDoQ:
				port = "853"
			default:
				return nil, errors.New("DNSCrypt stamp needs its server address")
			}
		} else {
			u, parseErr := url.Parse(item.URL)
			if parseErr != nil {
				return nil, parseErr
			}
			host = u.Hostname()
			port = u.Port()
			if net.ParseIP(host) != nil {
				args = append(args, "--upstream="+item.URL)
				continue
			}
			if u.User != nil {
				return nil, errors.New("use a DNS stamp for authenticated endpoints in the console runtime")
			}
			stamp.ProviderName = host
			switch u.Scheme {
			case "https":
				stamp.Proto = dnsstamps.StampProtoTypeDoH
				stamp.Path = u.RequestURI()
				if port == "" {
					port = "443"
				}
				if port != "443" {
					stamp.ProviderName = net.JoinHostPort(host, port)
				}
			case "tls":
				stamp.Proto = dnsstamps.StampProtoTypeTLS
				if port == "" {
					port = "853"
				}
			case "quic":
				stamp.Proto = dnsstamps.StampProtoTypeDoQ
				if port == "" {
					port = "853"
				}
			default:
				return nil, errors.New("unsupported encrypted upstream")
			}
		}
		addresses := append([]string(nil), item.Bootstrap...)
		if len(addresses) == 0 {
			addresses = appconfig.BootstrapForHost(host)
		}
		if len(addresses) == 0 {
			ips, lookupErr := dnsengine.ResolveHostViaBootstrapDNS(ctx, c.BootstrapDNS, host, c.Timeout, logger)
			if lookupErr != nil {
				return nil, lookupErr
			}
			for _, ip := range ips {
				addresses = append(addresses, ip.String())
			}
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("no bootstrap address for %s", host)
		}
		for _, address := range addresses {
			ip := address
			if h, _, splitErr := net.SplitHostPort(address); splitErr == nil {
				ip = h
			}
			if net.ParseIP(ip) == nil {
				return nil, fmt.Errorf("invalid bootstrap IP: %s", address)
			}
			stamp.ServerAddrStr = net.JoinHostPort(ip, port)
			args = append(args, "--upstream="+stamp.String())
		}
	}
	return args, nil
}

func (e *consoleEngine) startGeneration(ctx context.Context, c appconfig.Config) (*helperGeneration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args, err := officialUpstreamArgs(ctx, c, e.logger)
	if err != nil {
		return nil, err
	}
	dir, err := componentPath(e.root, e.lock.Components["dnsproxy"])
	if err != nil {
		return nil, err
	}
	var address string
	// dnsproxy binds both protocols to one port. A free TCP port may already
	// be occupied by a UDP client, particularly during concurrent test loads.
	for i := 0; i < 20; i++ {
		listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
		if listenErr != nil {
			return nil, listenErr
		}
		address = listener.Addr().String()
		packet, packetErr := net.ListenPacket("udp", address)
		_ = listener.Close()
		if packetErr == nil {
			_ = packet.Close()
			break
		}
		address = ""
	}
	if address == "" {
		return nil, errors.New("no free TCP/UDP helper port")
	}
	_, port, _ := net.SplitHostPort(address)
	timeout := 2 * time.Second
	if c.Timeout > 0 && c.Timeout < timeout {
		timeout = c.Timeout
	}
	args = append(args, "--listen=127.0.0.1", "--port="+port, "--upstream-mode=parallel", "--timeout="+timeout.String(), "--hosts-file-enabled=false", "--cache=false", "--pending-requests-enabled=false", "--dnssec=false", "--http3")
	args = append(args, e.extraArgs...)
	child, err := startChild(filepath.Join(dir, "dnsproxy.exe"), args, e.logger)
	if err != nil {
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		conn, dialErr := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(readyCtx, "tcp", address)
		if dialErr == nil {
			_ = conn.Close()
			return &helperGeneration{child: child, address: address, dnssec: appconfig.DNSSECEnabled(c), upstreams: c.Upstreams}, nil
		}
		if !child.alive() {
			child.stop()
			return nil, fmt.Errorf("official dnsproxy exited: %v", child.err)
		}
		select {
		case <-readyCtx.Done():
			child.stop()
			return nil, readyCtx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (e *consoleEngine) Start(ctx context.Context) error {
	if err := e.cfg.Normalize(); err != nil {
		return err
	}
	if err := verifyInstalled(e.root, e.lock); err != nil {
		return err
	}
	g, err := e.startGeneration(ctx, e.cfg)
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", e.cfg.Listen)
	if err != nil {
		g.child.stop()
		return err
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		_ = tcp.Close()
		g.child.stop()
		return err
	}
	e.cfg.Listen = tcp.Addr().String()
	e.generation = g
	handler := dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		ctx, cancel := context.WithTimeout(e.ctx, e.cfg.Timeout)
		defer cancel()
		r, _ := e.resolve(ctx, q, w.LocalAddr().Network())
		if r != nil {
			if _, ok := w.RemoteAddr().(*net.UDPAddr); ok {
				size := uint16(512)
				if opt := q.IsEdns0(); opt != nil {
					size = opt.UDPSize()
					if size < 512 {
						size = 512
					}
				}
				r.Truncate(int(size))
			}
			_ = w.WriteMsg(r)
		}
	})
	e.tcp = &dns.Server{Listener: tcp, Handler: handler}
	e.udp = &dns.Server{PacketConn: udp, Handler: handler, UDPSize: 65535}
	go func() {
		if err := e.tcp.ActivateAndServe(); err != nil {
			e.logger.Printf("TCP listener: %v", err)
		}
	}()
	go func() {
		if err := e.udp.ActivateAndServe(); err != nil {
			e.logger.Printf("UDP listener: %v", err)
		}
	}()
	e.ipc = control.NewServer(e.ring, e)
	e.ipc.PipeName = e.pipe
	if err := e.ipc.Start(); err != nil {
		_ = e.Shutdown(context.Background())
		return fmt.Errorf("start DNS control pipe: %w", err)
	}
	e.monitor.Add(1)
	go e.watchHelper()
	return nil
}

func (e *consoleEngine) watchHelper() {
	defer e.monitor.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
		e.changes.Lock()
		e.mu.RLock()
		alive := e.generation != nil && e.generation.child.alive()
		c := e.cfg.Clone()
		e.mu.RUnlock()
		if alive {
			e.changes.Unlock()
			failures = 0
			continue
		}
		if failures >= 4 {
			e.changes.Unlock()
			continue
		}
		failures++
		ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
		g, err := e.startGeneration(ctx, c)
		if err == nil {
			err = e.mu.LockContext(ctx)
			if err == nil {
				if !e.closed {
					old := e.generation
					e.generation = g
					if old != nil {
						old.child.stop()
					}
				} else {
					g.child.stop()
				}
				e.mu.Unlock()
			} else {
				g.child.stop()
			}
		}
		cancel()
		e.changes.Unlock()
		if err != nil {
			e.logger.Printf("dnsproxy restart %d failed: %v", failures, err)
		}
	}
}

func (e *consoleEngine) SetUpstreams(ctx context.Context, items []appconfig.Upstream) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stopCancel := context.AfterFunc(e.ctx, cancel)
	defer stopCancel()
	e.changes.Lock()
	defer e.changes.Unlock()
	e.mu.RLock()
	c := e.cfg.Clone()
	e.mu.RUnlock()
	c.Upstreams = items
	if err := c.Normalize(); err != nil {
		return err
	}
	g, err := e.startGeneration(ctx, c)
	if err != nil {
		return err
	}
	if len(c.Upstreams) > 0 {
		if err = probeConsoleDNS(ctx, g.address); err != nil {
			g.child.stop()
			return fmt.Errorf("upstream DNS check failed: %w", err)
		}
	}
	if err = e.mu.LockContext(ctx); err != nil {
		g.child.stop()
		return err
	}
	defer e.mu.Unlock()
	if e.closed {
		g.child.stop()
		return errors.New("runtime stopped")
	}
	old := e.generation
	e.generation = g
	e.cfg.Upstreams = c.Upstreams
	if old != nil {
		old.child.stop()
	}
	return nil
}

func (e *consoleEngine) ResolveMsg(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	return e.resolve(ctx, q, "windivert")
}

func (e *consoleEngine) resolve(ctx context.Context, q *dns.Msg, protocol string) (r *dns.Msg, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopCancel := context.AfterFunc(e.ctx, cancel)
	defer stopCancel()
	started := time.Now()
	e.mu.RLock()
	defer e.mu.RUnlock()
	g := e.generation
	var attempts []requestlog.Attempt
	defer func() {
		if len(q.Question) == 0 {
			return
		}
		result := dnsengine.ResponseCode(r)
		if err != nil {
			result = "ERROR"
			if dnsengine.IsTimeoutError(err) {
				result = "TIMEOUT"
			}
		}
		entry := requestlog.Entry{Time: started, CompletedTime: time.Now(), Domain: strings.TrimSuffix(q.Question[0].Name, "."), Type: dns.TypeToString[q.Question[0].Qtype], Protocol: protocol, Result: result, RCode: dnsengine.ResponseCode(r), DurationUS: time.Since(started).Microseconds(), AnswerIPs: dnsengine.AnswerIPs(r), Attempts: attempts, AttemptCount: len(attempts)}
		if err != nil {
			entry.Error = err.Error()
		}
		e.ring.Add(entry)
	}()
	if e.closed || g == nil || !g.child.alive() {
		err = errors.New("dnsproxy helper is not running")
	} else {
		for attempt := 1; attempt <= 2; attempt++ {
			if err = ctx.Err(); err != nil {
				break
			}
			query := q.Copy()
			if g.dnssec {
				if opt := query.IsEdns0(); opt != nil {
					opt.SetDo()
				} else {
					query.SetEdns0(1232, true)
				}
			}
			attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			at := time.Now()
			r, _, err = (&dns.Client{Net: "tcp", Timeout: 2 * time.Second}).ExchangeContext(attemptCtx, query, g.address)
			cancel()
			info := requestlog.Attempt{Number: attempt, DurationUS: time.Since(at).Microseconds()}
			for _, u := range g.upstreams {
				info.Upstreams = append(info.Upstreams, u.URL)
			}
			if err != nil {
				info.Error = err.Error()
			}
			attempts = append(attempts, info)
			if err == nil && r != nil && r.Rcode != dns.RcodeServerFailure {
				break
			}
			if err == nil {
				err = errors.New("upstream returned SERVFAIL")
			}
		}
	}
	if err != nil {
		r = new(dns.Msg)
		r.SetReply(q)
		r.Rcode = dns.RcodeServerFailure
		r.RecursionAvailable = true
	}
	if r != nil {
		restoreClientDNSFlags(q, r)
	}
	return r, err
}

func restoreClientDNSFlags(q, r *dns.Msg) {
	do := q.IsEdns0() != nil && q.IsEdns0().Do()
	if !do && !q.AuthenticatedData {
		r.AuthenticatedData = false
	}
	var except uint16
	if len(q.Question) > 0 {
		except = q.Question[0].Qtype
	}
	filter := func(rows []dns.RR, extra bool) []dns.RR {
		out := rows[:0]
		for _, rr := range rows {
			kind := rr.Header().Rrtype
			if kind == dns.TypeOPT {
				if extra && q.IsEdns0() != nil {
					opt := rr.(*dns.OPT)
					opt.SetDo(do)
					out = append(out, rr)
				}
				continue
			}
			isSEC := kind == dns.TypeRRSIG || kind == dns.TypeSIG || kind == dns.TypeDNSKEY || kind == dns.TypeDS || kind == dns.TypeNSEC || kind == dns.TypeNSEC3
			if !do && isSEC && kind != except {
				continue
			}
			out = append(out, rr)
		}
		return out
	}
	r.Answer = filter(r.Answer, false)
	r.Ns = filter(r.Ns, false)
	r.Extra = filter(r.Extra, true)
}

func (e *consoleEngine) RuntimeState() any {
	e.mu.RLock()
	defer e.mu.RUnlock()
	alive := e.generation != nil && e.generation.child.alive()
	return map[string]any{"build": buildinfo.Version, "listen": e.cfg.Listen, "provider": e.cfg.CurrentProvider(), "helper_running": alive, "dnssec": appconfig.DNSSECEnabled(e.cfg), "cache": false, "log_rows": requestlog.Capacity}
}

func (e *consoleEngine) Shutdown(ctx context.Context) error {
	e.cancel()
	if e.ipc != nil {
		e.ipc.Close()
	}
	e.monitor.Wait()
	var result error
	if e.tcp != nil {
		result = e.tcp.ShutdownContext(ctx)
	}
	if e.udp != nil {
		result = errors.Join(result, e.udp.ShutdownContext(ctx))
	}
	// Queries inherit e.ctx and are cancelled above; drain before killing the helper.
	if err := e.mu.LockContext(context.Background()); err != nil {
		return errors.Join(result, err)
	}
	e.closed = true
	if e.generation != nil {
		e.generation.child.stop()
	}
	e.mu.Unlock()
	return result
}

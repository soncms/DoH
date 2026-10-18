package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const bootstrapConcurrency = 4

func resolveHostViaBootstrapDNS(ctx context.Context, servers []string, host string, timeout time.Duration, logger *log.Logger) ([]netip.Addr, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if len(servers) == 0 {
		servers = DefaultBootstrapDNS
	}
	clientTimeout := min(timeout, 1500*time.Millisecond)
	for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
		ips, server := raceBootstrapServers(ctx, servers, dns.Fqdn(host), clientTimeout)
		if len(ips) > 0 {
			if logger != nil {
				logger.Printf("bootstrap DNS: resolved %s via %s to %v", host, server, ips)
			}
			return ips, nil
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	// Keep the existing system-resolver fallback inside the same overall budget.
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err == nil && len(ips) > 0 {
		return ips, nil
	}
	return nil, fmt.Errorf("bootstrap lookup %s via %v failed: %w", host, servers, err)
}

func raceBootstrapServers(ctx context.Context, servers []string, fqdn string, timeout time.Duration) ([]netip.Addr, string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		ips    []netip.Addr
		server string
	}
	results := make(chan result, min(bootstrapConcurrency, len(servers)))
	jobs := make(chan string)
	var workers sync.WaitGroup
	for i := 0; i < min(bootstrapConcurrency, len(servers)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for server := range jobs {
				ips, _ := queryBootstrapServer(ctx, server, fqdn, timeout)
				select {
				case results <- result{ips, server}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, server := range servers {
			select {
			case jobs <- server:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	for {
		select {
		case <-ctx.Done():
			return nil, ""
		case res, ok := <-results:
			if !ok {
				return nil, ""
			}
			if len(res.ips) > 0 {
				cancel()
				workers.Wait()
				return res.ips, res.server
			}
		}
	}
}

func queryBootstrapServer(ctx context.Context, server, fqdn string, timeout time.Duration) ([]netip.Addr, error) {
	var lastErr error
	for _, network := range []string{"udp", "tcp"} {
		var found []netip.Addr
		client := &dns.Client{Net: network, Timeout: timeout}
		for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
			if err := ctx.Err(); err != nil {
				return found, err
			}
			msg := new(dns.Msg)
			msg.SetQuestion(fqdn, qtype)
			resp, err := exchangeBootstrapDNS(ctx, client, msg, server)
			if err != nil {
				lastErr = err
				continue
			}
			if resp == nil || resp.Truncated || resp.Rcode != dns.RcodeSuccess {
				continue
			}
			for _, rr := range resp.Answer {
				var ip net.IP
				switch v := rr.(type) {
				case *dns.A:
					ip = v.A
				case *dns.AAAA:
					ip = v.AAAA
				}
				if addr, ok := netip.AddrFromSlice(ip); ok {
					found = append(found, addr.Unmap())
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

func exchangeBootstrapDNS(ctx context.Context, client *dns.Client, msg *dns.Msg, server string) (*dns.Msg, error) {
	conn, err := client.DialContext(ctx, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// miekg/dns applies context deadlines, but cancellation without a changed
	// deadline also needs to interrupt an already blocked read.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	resp, _, err := client.ExchangeWithConnContext(ctx, msg, conn)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return resp, err
}

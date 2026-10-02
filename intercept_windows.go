//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
)

const winDivertOutbound = uint32(1 << 17)

// WinDivert 2.x WINDIVERT_ADDRESS is 80 bytes. Its network data starts at
// offset 16; the older 1.x bindings have a different layout and call ABI.
type winDivertAddress struct {
	Timestamp int64
	Flags     uint32
	Reserved  uint32
	Network   [64]byte
}

// Interceptor answers outbound IPv4/IPv6 UDP DNS directly through the DoH
// resolver. It preserves the requested server address in the reply, so no
// adapter DNS setting or port-53 listener needs to be changed.
type Interceptor struct {
	handle   windows.Handle
	recv     *windows.LazyProc
	send     *windows.LazyProc
	checksum *windows.LazyProc
	shutdown *windows.LazyProc
	close    *windows.LazyProc
	resolver *Resolver
	log      *log.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	workers  chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewInterceptor(c Config, resolver *Resolver, logger *log.Logger) (*Interceptor, error) {
	dir, err := resolveWinDivertDir()
	if err != nil {
		return nil, fmt.Errorf("prepare WinDivert: %w", err)
	}
	dll := windows.NewLazyDLL(filepath.Join(dir, "WinDivert.dll"))
	open := dll.NewProc("WinDivertOpen")
	i := &Interceptor{
		recv: dll.NewProc("WinDivertRecv"), send: dll.NewProc("WinDivertSend"),
		checksum: dll.NewProc("WinDivertHelperCalcChecksums"),
		shutdown: dll.NewProc("WinDivertShutdown"), close: dll.NewProc("WinDivertClose"),
		resolver: resolver, log: logger, workers: make(chan struct{}, 1024),
	}
	for _, proc := range []*windows.LazyProc{open, i.recv, i.send, i.checksum, i.shutdown, i.close} {
		if err := proc.Find(); err != nil {
			return nil, fmt.Errorf("load WinDivert 2.x: %w", err)
		}
	}
	// Exclude reinjected packets so malformed/unsupported datagrams can pass
	// through without being captured repeatedly by this same handle.
	filter, err := windows.BytePtrFromString("outbound and !loopback and !impostor and udp.DstPort == 53")
	if err != nil {
		return nil, err
	}
	handle, _, callErr := open.Call(uintptr(unsafe.Pointer(filter)), 0, 0, 0)
	if handle == uintptr(windows.InvalidHandle) {
		return nil, fmt.Errorf("open WinDivert (Administrator required): %w", callErr)
	}
	i.handle = windows.Handle(handle)
	i.ctx, i.cancel = context.WithCancel(context.Background())
	return i, nil
}

func (i *Interceptor) Start() error {
	i.wg.Add(1)
	go i.receiveLoop()
	return nil
}

func (i *Interceptor) receiveLoop() {
	defer i.wg.Done()
	recvBuf := make([]byte, 65535)
	for i.ctx.Err() == nil {
		var n uint32
		var addr winDivertAddress
		// WinDivert 2.x: pRecvLen comes BEFORE pAddr.
		ok, _, err := i.recv.Call(uintptr(i.handle), uintptr(unsafe.Pointer(&recvBuf[0])), uintptr(len(recvBuf)), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&addr)))
		if ok == 0 {
			if i.ctx.Err() == nil {
				i.log.Printf("WinDivert receive failed: %v", err)
			}
			return
		}
		if n == 0 || n > uint32(len(recvBuf)) {
			continue
		}
		raw := make([]byte, n)
		copy(raw, recvBuf[:n])
		if i.ctx.Err() != nil {
			_ = i.sendPacket(raw, &addr)
			return
		}
		packet, err := parseDNSDatagram(raw)
		if err != nil {
			i.resolver.recordInterceptIssue("<packet>", "UNKNOWN", "BYPASS", err)
			// Unsupported fragments/extensions must not be silently dropped.
			if err := i.sendPacket(raw, &addr); err != nil {
				i.log.Printf("reinject unsupported packet: %v", err)
			}
			continue
		}
		select {
		case i.workers <- struct{}{}:
		case <-i.ctx.Done():
			_ = i.sendPacket(raw, &addr)
			return
		}
		i.wg.Add(1)
		go func() {
			defer i.wg.Done()
			defer func() { <-i.workers }()
			i.answer(packet, addr)
		}()
	}
}

func (i *Interceptor) answer(packet dnsDatagram, addr winDivertAddress) {
	request := new(dns.Msg)
	if err := request.Unpack(packet.payload); err != nil || request.Response || len(request.Question) == 0 {
		// The filter is intentionally broad at UDP/53. Preserve packets that
		// are not client DNS queries so interception cannot black-hole a valid
		// response, a non-DNS payload, or a newer DNS extension we do not parse.
		if err := i.sendPacket(packet.raw, &addr); err != nil {
			i.log.Printf("reinject non-query DNS packet: %v", err)
		}
		return
	}
	wire, err := i.resolver.Resolve(i.ctx, packet.payload)
	if i.ctx.Err() != nil {
		return
	}
	if err != nil || len(wire) == 0 {
		if i.resolver != nil && i.resolver.engine == nil {
			i.resolver.recordInterceptIssue(request.Question[0].Name, dns.TypeToString[request.Question[0].Qtype], "ERROR", err)
		}
		return
	}
	response := new(dns.Msg)
	if err := response.Unpack(wire); err != nil {
		if i.resolver != nil && i.resolver.engine == nil {
			i.resolver.recordInterceptIssue(request.Question[0].Name, dns.TypeToString[request.Question[0].Qtype], "ERROR", err)
		}
		i.log.Printf("unpack intercepted DNS response: %v", err)
		return
	}
	response.Id = request.Id
	wire, err = response.Pack()
	if err != nil {
		i.log.Printf("pack intercepted DNS response: %v", err)
		return
	}
	raw, err := packet.reply(wire)
	if err != nil {
		i.log.Printf("build intercepted DNS response: %v", err)
		return
	}
	addr.Flags &^= winDivertOutbound
	ok, _, err := i.checksum.Call(uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)), uintptr(unsafe.Pointer(&addr)), 0)
	if ok == 0 {
		i.log.Printf("calculate DNS packet checksum: %v", err)
		return
	}
	if err := i.sendPacket(raw, &addr); err != nil {
		i.log.Printf("inject DoH response: %v", err)
		return
	}
}

func (i *Interceptor) sendPacket(raw []byte, addr *winDivertAddress) error {
	if len(raw) == 0 {
		return nil
	}
	var n uint32
	// WinDivert 2.x: pSendLen comes BEFORE pAddr.
	ok, _, err := i.send.Call(uintptr(i.handle), uintptr(unsafe.Pointer(&raw[0])), uintptr(len(raw)), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(addr)))
	if ok == 0 {
		return err
	}
	return nil
}

func (i *Interceptor) Shutdown() {
	if i == nil {
		return
	}
	i.stopOnce.Do(func() {
		i.cancel()
		if i.handle != 0 && i.handle != windows.InvalidHandle {
			_ = windows.CancelIoEx(i.handle, nil)
			if i.shutdown != nil {
				_, _, _ = i.shutdown.Call(uintptr(i.handle), 3) // WINDIVERT_SHUTDOWN_BOTH
			}
			go func() {
				if conn, err := net.Dial("udp", "127.0.0.1:53"); err == nil {
					_, _ = conn.Write([]byte{0})
					_ = conn.Close()
				}
				if conn, err := net.Dial("udp", "1.1.1.1:53"); err == nil {
					_, _ = conn.Write([]byte{0})
					_ = conn.Close()
				}
			}()
			if i.close != nil {
				_, _, _ = i.close.Call(uintptr(i.handle))
			}
		}
		done := make(chan struct{})
		go func() {
			i.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			if i.log != nil {
				i.log.Printf("WinDivert shutdown wait timed out")
			}
		}
	})
}

func resolveWinDivertDir() (string, error) {
	progData := os.Getenv("ProgramData")
	if progData == "" {
		return "", errors.New("ProgramData is unavailable; refusing to extract WinDivert runtime")
	}
	appDir := filepath.Join(progData, "SonDNS")
	if len(embeddedWinDivertDLL) > 0 && len(embeddedWinDivertSYS) > 0 {
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			return "", err
		}
		dllPath := filepath.Join(appDir, "WinDivert.dll")
		sysPath := filepath.Join(appDir, "WinDivert64.sys")
		// Always refresh the pair from the embedded build. This keeps the DNS
		// interceptor and winws2 on the same WinDivert ABI after an upgrade.
		if err := writeEmbeddedAsset(dllPath, embeddedWinDivertDLL, 0o755); err != nil {
			return "", fmt.Errorf("extract WinDivert.dll: %w", err)
		}
		if err := writeEmbeddedAsset(sysPath, embeddedWinDivertSYS, 0o644); err != nil {
			return "", fmt.Errorf("extract WinDivert64.sys: %w", err)
		}
		return appDir, nil
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if hasWinDivertFiles(exeDir) {
			return exeDir, nil
		}
	}
	if hasWinDivertFiles(appDir) {
		return appDir, nil
	}
	return "", errors.New("missing WinDivert.dll and WinDivert64.sys")
}

func hasWinDivertFiles(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "WinDivert.dll")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "WinDivert64.sys")); err != nil {
		return false
	}
	return true
}

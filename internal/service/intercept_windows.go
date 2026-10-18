//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/miekg/dns"
	"golang.org/x/sys/windows"
	resources "sondns"
	appconfig "sondns/internal/config"
	dnsengine "sondns/internal/engine"
	dnspacket "sondns/internal/packet"
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

// Only handle/scalar operations use this interface. Buffer syscalls stay on
// concrete LazyProc methods so Go preserves their uintptr escape annotations.
type winDivertHandleProc interface {
	Call(...uintptr) (uintptr, uintptr, error)
}

// Interceptor answers outbound IPv4/IPv6 UDP DNS directly through the DoH
// resolver. It preserves the requested server address in the reply, so no
// adapter DNS setting or port-53 listener needs to be changed.
type Interceptor struct {
	handle   windows.Handle
	recv     *windows.LazyProc
	send     *windows.LazyProc
	checksum *windows.LazyProc
	shutdown winDivertHandleProc
	close    winDivertHandleProc
	resolver *dnsengine.Resolver
	log      *log.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	workers  chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func NewInterceptor(c appconfig.Config, resolver *dnsengine.Resolver, logger *log.Logger) (*Interceptor, error) {
	dir, err := resolveWinDivertDir(logger)
	if err != nil {
		return nil, fmt.Errorf("prepare WinDivert: %w", err)
	}
	dll := windows.NewLazyDLL(filepath.Join(dir, "WinDivert.dll"))
	open := dll.NewProc("WinDivertOpen")
	recv, send := dll.NewProc("WinDivertRecv"), dll.NewProc("WinDivertSend")
	checksum := dll.NewProc("WinDivertHelperCalcChecksums")
	shutdown, close := dll.NewProc("WinDivertShutdown"), dll.NewProc("WinDivertClose")
	i := &Interceptor{
		recv: recv, send: send, checksum: checksum, shutdown: shutdown, close: close,
		resolver: resolver, log: logger, workers: make(chan struct{}, 1024),
	}
	for _, proc := range []*windows.LazyProc{open, recv, send, checksum, shutdown, close} {
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
		packet, err := dnspacket.Parse(raw)
		if err != nil {
			i.resolver.RecordInterceptIssue("<packet>", "UNKNOWN", "BYPASS", err)
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

func (i *Interceptor) answer(packet dnspacket.Datagram, addr winDivertAddress) {
	request := new(dns.Msg)
	if err := request.Unpack(packet.Payload); err != nil || request.Response || len(request.Question) == 0 {
		// The filter is intentionally broad at UDP/53. Preserve packets that
		// are not client DNS queries so interception cannot black-hole a valid
		// response, a non-DNS payload, or a newer DNS extension we do not parse.
		if err := i.sendPacket(packet.Raw, &addr); err != nil {
			i.log.Printf("reinject non-query DNS packet: %v", err)
		}
		return
	}
	wire, err := i.resolver.Resolve(i.ctx, packet.Payload)
	if i.ctx.Err() != nil {
		return
	}
	if err != nil || len(wire) == 0 {
		if i.resolver != nil && i.resolver.Engine == nil {
			i.resolver.RecordInterceptIssue(request.Question[0].Name, dns.TypeToString[request.Question[0].Qtype], "ERROR", err)
		}
		return
	}
	response := new(dns.Msg)
	if err := response.Unpack(wire); err != nil {
		if i.resolver != nil && i.resolver.Engine == nil {
			i.resolver.RecordInterceptIssue(request.Question[0].Name, dns.TypeToString[request.Question[0].Qtype], "ERROR", err)
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
	raw, err := packet.Reply(wire)
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

func resolveWinDivertDir(logger *log.Logger) (string, error) {
	if ConsoleBuild {
		return portableRuntimeDir()
	}
	return extractEmbeddedWinDivertAt(PortableRoot(), logger)
}

func extractEmbeddedWinDivertAt(installRoot string, logger *log.Logger) (string, error) {
	appDir := embeddedRuntimePath(installRoot)
	if len(resources.EmbeddedWinDivertDLL) > 0 && len(resources.EmbeddedWinDivertSYS) > 0 {
		if err := ensurePortableDirectory(installRoot, appDir); err != nil {
			return "", err
		}
		dllPath := filepath.Join(appDir, "WinDivert.dll")
		sysPath := filepath.Join(appDir, "WinDivert64.sys")
		// Always refresh the pair from the embedded build. This keeps the DNS
		// interceptor and winws2 on the same WinDivert ABI after an upgrade.
		if err := writeEmbeddedAsset(logger, "third_party/zapret2/WinDivert.dll", dllPath, resources.EmbeddedWinDivertDLL, 0o755); err != nil {
			return "", fmt.Errorf("extract WinDivert.dll: %w", err)
		}
		if err := writeEmbeddedAsset(logger, "third_party/zapret2/WinDivert64.sys", sysPath, resources.EmbeddedWinDivertSYS, 0o644); err != nil {
			return "", fmt.Errorf("extract WinDivert64.sys: %w", err)
		}
		return appDir, nil
	}
	if hasWinDivertFiles(appDir) {
		if err := verifyWinDivertFiles(appDir); err != nil {
			return "", fmt.Errorf("verify portable WinDivert: %w", err)
		}
		return appDir, nil
	}
	return "", errors.New("missing WinDivert.dll and WinDivert64.sys")
}

func verifyWinDivertFiles(dir string) error {
	if err := verifyRuntimeAssetFile("third_party/zapret2/WinDivert.dll", filepath.Join(dir, "WinDivert.dll")); err != nil {
		return err
	}
	return verifyRuntimeAssetFile("third_party/zapret2/WinDivert64.sys", filepath.Join(dir, "WinDivert64.sys"))
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

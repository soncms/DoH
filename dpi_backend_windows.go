//go:build windows

package main

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const embeddedDPIBackend = "embedded-zapret2"

// DPIBackend owns the lifecycle of the embedded winws2 engine or an explicitly
// configured external-compatible engine. The embedded path is the default and
// is extracted to ProgramData at runtime so the distributable remains one EXE.
type DPIBackend struct {
	cfg    DPIConfig
	logger *log.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	stop    chan struct{}
	done    chan struct{}
	stopOne sync.Once
}

func NewDPIBackend(c Config, logger *log.Logger) (*DPIBackend, error) {
	if !c.DPI.Enabled {
		return nil, nil
	}
	if logger == nil {
		return nil, errors.New("dpi backend requires a logger")
	}
	return &DPIBackend{cfg: c.DPI, logger: logger}, nil
}

func (b *DPIBackend) Start() error {
	if b == nil {
		return nil
	}
	exe, args, err := b.command()
	if err != nil {
		b.logger.Printf("DPI backend unavailable: %v; continuing in fail-open mode", err)
		return nil
	}
	b.stop = make(chan struct{})
	b.done = make(chan struct{})
	go b.run(exe, args)
	return nil
}

func (b *DPIBackend) command() (string, []string, error) {
	if b.cfg.Backend == embeddedDPIBackend {
		root, err := ensureEmbeddedZapret2(b.logger)
		if err != nil {
			return "", nil, err
		}
		args, err := zapret2Arguments(root, b.cfg)
		if err != nil {
			return "", nil, err
		}
		return filepath.Join(root, "winws2.exe"), args, nil
	}
	exe := resolveDPIExecutable(b.cfg.Executable)
	if exe == "" {
		return "", nil, errors.New("configured DPI helper executable was not found")
	}
	return exe, append([]string(nil), b.cfg.Arguments...), nil
}

func (b *DPIBackend) run(exe string, args []string) {
	defer close(b.done)
	for attempt := 0; ; attempt++ {
		cmd := exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(exe)
		cmd.Stdout = logWriter{logger: b.logger, prefix: "dpi: "}
		cmd.Stderr = logWriter{logger: b.logger, prefix: "dpi: "}
		b.mu.Lock()
		b.cmd = cmd
		b.mu.Unlock()

		b.logger.Printf("starting DPI backend %q", exe)
		err := cmd.Start()
		if err == nil {
			err = cmd.Wait()
		}
		b.mu.Lock()
		b.cmd = nil
		b.mu.Unlock()

		select {
		case <-b.stop:
			return
		default:
		}
		if err != nil {
			b.logger.Printf("DPI backend exited: %v", err)
		} else {
			b.logger.Printf("DPI backend exited")
		}
		if !b.cfg.AutoRestart || attempt >= 4 {
			return
		}
		backoff := time.Duration(attempt+1) * time.Second
		select {
		case <-time.After(backoff):
		case <-b.stop:
			return
		}
	}
}

func (b *DPIBackend) Shutdown() {
	if b == nil || b.done == nil {
		return
	}
	b.stopOne.Do(func() {
		close(b.stop)
		b.mu.Lock()
		cmd := b.cmd
		b.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				b.logger.Printf("stop DPI backend: %v", err)
			}
		}
		select {
		case <-b.done:
		case <-time.After(5 * time.Second):
			b.logger.Printf("timed out waiting for DPI backend")
		}
	})
}

func ensureEmbeddedZapret2(logger *log.Logger) (string, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		return "", errors.New("ProgramData is unavailable; refusing to extract DPI runtime")
	}
	root := filepath.Join(base, "SonDNS")
	assetDir := filepath.Join(root, "zapret2")
	for _, dir := range []string{root, filepath.Join(assetDir, "lua"), filepath.Join(assetDir, "windivert.filter")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	assets := []struct {
		name string
		path string
		data []byte
		mode os.FileMode
	}{
		{"third_party/zapret2/winws2.exe", filepath.Join(root, "winws2.exe"), embeddedZapret2WinWS, 0o755},
		{"third_party/zapret2/cygwin1.dll", filepath.Join(root, "cygwin1.dll"), embeddedZapret2Cygwin, 0o644},
		{"third_party/zapret2/lua/zapret-lib.lua", filepath.Join(assetDir, "lua", "zapret-lib.lua"), embeddedZapret2LibLua, 0o644},
		{"third_party/zapret2/lua/zapret-antidpi.lua", filepath.Join(assetDir, "lua", "zapret-antidpi.lua"), embeddedZapret2AntiDPILua, 0o644},
		{"third_party/zapret2/windivert.filter/windivert_part.discord_media.txt", filepath.Join(assetDir, "windivert.filter", "windivert_part.discord_media.txt"), embeddedZapret2DiscordFilter, 0o644},
		{"third_party/zapret2/windivert.filter/windivert_part.stun.txt", filepath.Join(assetDir, "windivert.filter", "windivert_part.stun.txt"), embeddedZapret2STUNFilter, 0o644},
		{"third_party/zapret2/windivert.filter/windivert_part.wireguard.txt", filepath.Join(assetDir, "windivert.filter", "windivert_part.wireguard.txt"), embeddedZapret2WireguardFilter, 0o644},
		{"third_party/zapret2/windivert.filter/windivert_part.quic_initial_ietf.txt", filepath.Join(assetDir, "windivert.filter", "windivert_part.quic_initial_ietf.txt"), embeddedZapret2QUICFilter, 0o644},
	}
	for _, asset := range assets {
		if err := writeEmbeddedAsset(logger, asset.name, asset.path, asset.data, asset.mode); err != nil {
			return "", err
		}
	}
	return root, nil
}

func zapret2Arguments(root string, cfg DPIConfig) ([]string, error) {
	if len(cfg.Arguments) > 0 {
		return append([]string(nil), cfg.Arguments...), nil
	}
	assetDir := filepath.Join(root, "zapret2")
	luaDir := filepath.Join(assetDir, "lua")
	filterDir := filepath.Join(assetDir, "windivert.filter")
	tcpCapture, tcpProfile := "80,443", "80,443"
	udpCapture := ""
	switch cfg.CaptureMode {
	case "tcp":
		tcpCapture, tcpProfile = "1-52,54-65535", "1-65535"
	case "tcp+udp":
		tcpCapture, tcpProfile, udpCapture = "1-52,54-65535", "1-65535", "443"
	}
	args := []string{
		"--wf-tcp-out=" + tcpCapture,
		"--wf-filter-lan=1",
		"--wf-filter-loopback=1",
		"--wf-tcp-empty=0",
		"--lua-init=@" + filepath.Join(luaDir, "zapret-lib.lua"),
		"--lua-init=@" + filepath.Join(luaDir, "zapret-antidpi.lua"),
		"--wf-raw-part=@" + filepath.Join(filterDir, "windivert_part.discord_media.txt"),
		"--wf-raw-part=@" + filepath.Join(filterDir, "windivert_part.stun.txt"),
		"--wf-raw-part=@" + filepath.Join(filterDir, "windivert_part.wireguard.txt"),
		"--wf-raw-part=@" + filepath.Join(filterDir, "windivert_part.quic_initial_ietf.txt"),
	}
	if udpCapture != "" {
		args = append(args, "--wf-udp-out="+udpCapture)
	}
	hostlist, err := zapret2Hostlist(root, "targets.txt", cfg.Targets)
	if err != nil {
		return nil, err
	}
	excludeList, err := zapret2Hostlist(root, "exclude.txt", cfg.Exclude)
	if err != nil {
		return nil, err
	}
	hostArg := ""
	if hostlist != "" {
		hostArg = "--hostlist=" + hostlist
	}
	excludeArg := ""
	if excludeList != "" {
		excludeArg = "--hostlist-exclude=" + excludeList
	}
	args = append(args,
		"--filter-tcp="+tcpProfile, "--filter-l7=http", hostArg, excludeArg, "--out-range=-d10", "--payload=http_req",
		"--lua-desync=fake:blob=fake_default_http:ip_autottl=-2,3-20:ip6_autottl=-2,3-20:tcp_md5",
		"--lua-desync=fakedsplit:ip_autottl=-2,3-20:ip6_autottl=-2,3-20:tcp_md5",
		"--new",
		"--filter-tcp="+tcpProfile, "--filter-l7=tls", hostArg, excludeArg, "--out-range=-d10", "--payload=tls_client_hello",
		"--lua-desync=fake:blob=fake_default_tls:tcp_md5:tcp_seq=-10000:repeats=6",
		"--lua-desync=multidisorder:pos=midsld",
		"--new",
		"--filter-udp=443", "--filter-l7=quic", hostArg, excludeArg, "--payload=quic_initial",
		"--lua-desync=fake:blob=fake_default_quic:repeats=11",
	)
	return removeEmptyArgs(args), nil
}

func zapret2Hostlist(root, filename string, targets []string) (string, error) {
	lines := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target != "" {
			lines = append(lines, target)
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	path := filepath.Join(root, "zapret2", filename)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\r\n")+"\r\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func removeEmptyArgs(args []string) []string {
	result := args[:0]
	for _, arg := range args {
		if arg != "" {
			result = append(result, arg)
		}
	}
	return result
}

func resolveDPIExecutable(configured string) string {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if path, err := filepath.Abs(configured); err == nil {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path
			}
		}
		if path, err := exec.LookPath(configured); err == nil {
			return path
		}
		return ""
	}
	if path, err := exec.LookPath("winws2.exe"); err == nil {
		return path
	}
	return ""
}

type logWriter struct {
	logger *log.Logger
	prefix string
}

func (w logWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	if text != "" && w.logger != nil {
		w.logger.Printf("%s%s", w.prefix, text)
	}
	return len(p), nil
}

var _ io.Writer = logWriter{}

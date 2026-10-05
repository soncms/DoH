//go:build windows

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sondnsPipeName = `\\.\pipe\SonDNS\control`

type dnsIPCRequest struct {
	Op          string     `json:"op"`
	InstanceID  string     `json:"instance_id,omitempty"`
	Version     uint64     `json:"version,omitempty"`
	Upstreams   []Upstream `json:"upstreams,omitempty"`
	Enabled     *bool      `json:"enabled,omitempty"`
	CaptureMode string     `json:"capture_mode,omitempty"`
}

type dnsIPCState struct {
	DNSRunning    bool   `json:"dns_running"`
	DPIRunning    bool   `json:"dpi_running"`
	DPIEnabled    bool   `json:"dpi_enabled"`
	CaptureMode   string `json:"capture_mode"`
	Provider      string `json:"provider"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	LastError     string `json:"last_error,omitempty"`
	AssetVerified bool   `json:"asset_verified"`
	AssetError    string `json:"asset_error,omitempty"`
}

type dnsIPCConfig struct {
	Provider         string     `json:"provider"`
	Upstreams        []Upstream `json:"upstreams"`
	DPIEnabled       bool       `json:"dpi_enabled"`
	CaptureMode      string     `json:"capture_mode"`
	ServiceAutoStart bool       `json:"service_auto_start"`
}

type dnsIPCDiagnostics struct {
	Version       string                   `json:"version"`
	BinarySHA256  string                   `json:"binary_sha256,omitempty"`
	SignerStatus  string                   `json:"signer_status,omitempty"`
	BuildInfoPath string                   `json:"build_info_path,omitempty"`
	BuildInfo     map[string]any           `json:"build_info,omitempty"`
	Assets        []runtimeAssetDiagnostic `json:"assets,omitempty"`
}

type dnsIPCResponse struct {
	OK              bool               `json:"ok"`
	Unchanged       bool               `json:"unchanged,omitempty"`
	InstanceID      string             `json:"instance_id,omitempty"`
	SchemaVersion   int                `json:"schema_version,omitempty"`
	Version         uint64             `json:"version,omitempty"`
	OverwrittenRows uint64             `json:"overwritten_rows,omitempty"`
	Entries         []DNSRequestEntry  `json:"entries,omitempty"`
	State           *dnsIPCState       `json:"state,omitempty"`
	Config          *dnsIPCConfig      `json:"config,omitempty"`
	Diagnostics     *dnsIPCDiagnostics `json:"diagnostics,omitempty"`
	NeedsRestart    bool               `json:"needs_restart,omitempty"`
	Code            string             `json:"code,omitempty"`
	Error           string             `json:"error,omitempty"`
}

type dnsEngineProxy interface {
	SetUpstreams([]Upstream) error
}

type dnsIPCServer struct {
	pipeName   string
	instanceID string
	ring       *DNSRequestRing
	proxy      dnsEngineProxy
	stop       chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	active     windows.Handle
	engine     *DNSProxyEngine
}

func newDNSIPCServer(ring *DNSRequestRing, proxy dnsEngineProxy) *dnsIPCServer {
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	return &dnsIPCServer{pipeName: sondnsPipeName, instanceID: id, ring: ring, proxy: proxy, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *dnsIPCServer) start() { go s.serve() }

func (s *dnsIPCServer) serve() {
	defer close(s.done)
	sd, _ := windows.SecurityDescriptorFromString("D:P(A;;GA;;;IU)")
	name, _ := windows.UTF16PtrFromString(s.pipeName)
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		h, err := windows.CreateNamedPipe(name,
			windows.PIPE_ACCESS_DUPLEX,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
			windows.PIPE_UNLIMITED_INSTANCES, 64*1024, 64*1024, 1000, sa)
		if err != nil {
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) { // another instance owns the pipe
				return
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		s.active = h
		s.mu.Unlock()
		err = windows.ConnectNamedPipe(h, nil)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(h)
			s.mu.Lock()
			s.active = 0
			s.mu.Unlock()
			continue
		}
		s.handle(h)
		s.mu.Lock()
		s.active = 0
		s.mu.Unlock()
	}
}

func (s *dnsIPCServer) handle(h windows.Handle) {
	f := os.NewFile(uintptr(h), "SonDNS IPC")
	if f == nil {
		_ = windows.CloseHandle(h)
		return
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	var req dnsIPCRequest
	if json.Unmarshal(line, &req) != nil {
		writeIPC(f, dnsIPCResponse{SchemaVersion: 3, Code: "INVALID_REQUEST", Error: "invalid request"})
		return
	}
	_ = writeIPC(f, s.respond(req))
}

func (s *dnsIPCServer) respond(req dnsIPCRequest) dnsIPCResponse {
	resp := dnsIPCResponse{SchemaVersion: 3}
	switch strings.ToLower(strings.TrimSpace(req.Op)) {
	case "snapshot":
		resp.OK = true
		curVer := s.ring.Version()
		if req.InstanceID == s.instanceID && req.Version == curVer {
			resp.Version = curVer
			_, _, resp.OverwrittenRows = s.ring.SnapshotStats()
			resp.Unchanged = true
		} else {
			resp.Entries, resp.Version, resp.OverwrittenRows = s.ring.SnapshotStats()
		}
	case "clear_log":
		resp.OK = true
		resp.Version = s.ring.Clear()
	case "get_state":
		if s.engine == nil {
			return s.fail(resp, "NOT_AVAILABLE", "runtime state unavailable")
		}
		resp.OK = true
		resp.State = s.engine.ipcState()
	case "get_config":
		if s.engine == nil {
			return s.fail(resp, "NOT_AVAILABLE", "runtime config unavailable")
		}
		resp.OK = true
		resp.Config = s.engine.ipcConfig()
	case "diagnostics":
		resp.OK = true
		version := buildVersion
		if envVersion := os.Getenv("SONDNS_VERSION"); envVersion != "" {
			version = envVersion
		}
		resp.Diagnostics = collectDNSDiagnostics(version)
	case "set_upstream":
		if s.proxy == nil {
			return s.fail(resp, "NOT_AVAILABLE", "proxy unavailable")
		} else if len(req.Upstreams) == 0 {
			return s.fail(resp, "VALIDATION_FAILED", "no upstreams provided")
		} else {
			var previous Config
			if s.engine != nil {
				previous = s.engine.configSnapshot()
			}
			if err := s.proxy.SetUpstreams(req.Upstreams); err != nil {
				return s.fail(resp, "UPSTREAM_APPLY_FAILED", err.Error())
			}
			if s.engine != nil {
				candidate := previous
				candidate.Upstreams = append([]Upstream(nil), req.Upstreams...)
				s.engine.setConfig(candidate)
				if err := s.engine.persistConfig(); err != nil {
					s.engine.setConfig(previous)
					_ = s.proxy.SetUpstreams(previous.Upstreams)
					return s.fail(resp, "CONFIG_SAVE_FAILED", err.Error())
				}
			}
			resp.OK = true
		}
	case "set_dpi":
		if s.engine == nil {
			return s.fail(resp, "NOT_AVAILABLE", "runtime config unavailable")
		}
		if req.Enabled == nil || req.CaptureMode == "" {
			return s.fail(resp, "VALIDATION_FAILED", "enabled and capture_mode are required")
		}
		candidate := s.engine.configSnapshot()
		candidate.DPI.Enabled = *req.Enabled
		candidate.DPI.CaptureMode = req.CaptureMode
		if err := candidate.normalize(); err != nil {
			return s.fail(resp, "VALIDATION_FAILED", err.Error())
		}
		previous := s.engine.configSnapshot()
		s.engine.setConfig(candidate)
		if err := s.engine.persistConfig(); err != nil {
			s.engine.setConfig(previous)
			return s.fail(resp, "CONFIG_SAVE_FAILED", err.Error())
		}
		resp.OK = true
		resp.NeedsRestart = true
	default:
		return s.fail(resp, "UNKNOWN_OPERATION", fmt.Sprintf("unknown operation %q", req.Op))
	}
	resp.InstanceID = s.instanceID
	return resp
}

func (s *dnsIPCServer) fail(resp dnsIPCResponse, code, message string) dnsIPCResponse {
	resp.OK = false
	resp.Code = code
	resp.Error = message
	resp.InstanceID = s.instanceID
	return resp
}

func collectDNSDiagnostics(version string) *dnsIPCDiagnostics {
	diagnostics := &dnsIPCDiagnostics{Version: version, SignerStatus: "unavailable", Assets: runtimeAssetDiagnostics()}
	if executable, err := os.Executable(); err == nil {
		if data, readErr := os.ReadFile(executable); readErr == nil {
			hash := sha256.Sum256(data)
			diagnostics.BinarySHA256 = hex.EncodeToString(hash[:])
		}
		dir := filepath.Dir(executable)
		candidates := []string{
			filepath.Join(dir, "build-info.json"),
			filepath.Join(dir, "..", "build-info.json"),
		}
		for _, candidate := range candidates {
			data, readErr := os.ReadFile(candidate)
			if readErr != nil {
				continue
			}
			var info map[string]any
			if json.Unmarshal(data, &info) == nil {
				diagnostics.BuildInfo = info
				diagnostics.BuildInfoPath = candidate
				if artifacts, ok := info["artifacts"].([]any); ok {
					for _, item := range artifacts {
						artifact, ok := item.(map[string]any)
						if !ok || artifact["artifact"] != filepath.Base(executable) {
							continue
						}
						if status, ok := artifact["status"].(string); ok {
							diagnostics.SignerStatus = status
						}
					}
				}
			}
			break
		}
	}
	return diagnostics
}

func writeIPC(f *os.File, v dnsIPCResponse) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

func (s *dnsIPCServer) close() {
	select {
	case <-s.stop:
		return
	default:
		close(s.stop)
	}
	s.mu.Lock()
	h := s.active
	s.active = 0
	s.mu.Unlock()
	if h != 0 && h != windows.InvalidHandle {
		_ = windows.CancelIoEx(h, nil)
		_ = windows.CloseHandle(h)
	}
	name, _ := windows.UTF16PtrFromString(s.pipeName)
	hWake, _ := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if hWake != 0 && hWake != windows.InvalidHandle {
		_ = windows.CloseHandle(hWake)
	}
	select {
	case <-s.done:
	case <-time.After(500 * time.Millisecond):
	}
}

func callDNSIPC(req dnsIPCRequest) (dnsIPCResponse, error) {
	return callDNSIPCAt(sondnsPipeName, req)
}

func callDNSIPCAt(pipeName string, req dnsIPCRequest) (dnsIPCResponse, error) {
	name, _ := windows.UTF16PtrFromString(pipeName)
	var h windows.Handle
	var err error
	deadline := time.Now().Add(1200 * time.Millisecond)
	for {
		h, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || time.Now().After(deadline) {
			return dnsIPCResponse{}, err
		}
		time.Sleep(25 * time.Millisecond)
	}
	f := os.NewFile(uintptr(h), "SonDNS IPC")
	if f == nil {
		windows.CloseHandle(h)
		return dnsIPCResponse{}, errors.New("invalid IPC handle")
	}
	defer f.Close()
	// Connection timeout alone does not bound a stalled server's reply. An
	// overlapped pipe lets Go cancel I/O when this deadline expires.
	timeout := 3 * time.Second
	if req.Op == "set_upstream" {
		timeout = 15 * time.Second
	}
	if err = f.SetDeadline(time.Now().Add(timeout)); err != nil {
		return dnsIPCResponse{}, err
	}
	b, _ := json.Marshal(req)
	b = append(b, '\n')
	if _, err = f.Write(b); err != nil {
		return dnsIPCResponse{}, err
	}
	line, err := bufio.NewReaderSize(f, 64*1024).ReadBytes('\n')
	if err != nil {
		return dnsIPCResponse{}, err
	}
	var resp dnsIPCResponse
	if err = json.Unmarshal(line, &resp); err != nil {
		return dnsIPCResponse{}, err
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

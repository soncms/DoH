//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sondnsPipeName = `\\.\pipe\SonDNS\control`

type dnsIPCRequest struct {
	Op        string     `json:"op"`
	Version   uint64     `json:"version,omitempty"`
	Upstreams []Upstream `json:"upstreams,omitempty"`
}
type dnsIPCResponse struct {
	OK         bool              `json:"ok"`
	InstanceID string            `json:"instance_id,omitempty"`
	Version    uint64            `json:"version,omitempty"`
	Entries    []DNSRequestEntry `json:"entries,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type dnsEngineProxy interface {
	ClearCache()
	SetUpstreams([]Upstream) error
}

type dnsIPCServer struct {
	instanceID string
	ring       *DNSRequestRing
	proxy      dnsEngineProxy
	stop       chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	active     windows.Handle
}

func newDNSIPCServer(ring *DNSRequestRing, proxy dnsEngineProxy) *dnsIPCServer {
	id := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	return &dnsIPCServer{instanceID: id, ring: ring, proxy: proxy, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *dnsIPCServer) start() { go s.serve() }

func (s *dnsIPCServer) serve() {
	defer close(s.done)
	sd, _ := windows.SecurityDescriptorFromString("D:P(A;;GA;;;IU)")
	name, _ := windows.UTF16PtrFromString(sondnsPipeName)
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
			windows.CloseHandle(h)
			s.mu.Lock()
			s.active = 0
			s.mu.Unlock()
			continue
		}
		s.handle(h)
		_ = windows.DisconnectNamedPipe(h)
		_ = windows.CloseHandle(h)
		s.mu.Lock()
		s.active = 0
		s.mu.Unlock()
	}
}

func (s *dnsIPCServer) handle(h windows.Handle) {
	f := os.NewFile(uintptr(h), "SonDNS IPC")
	if f == nil {
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
		writeIPC(f, dnsIPCResponse{Error: "invalid request"})
		return
	}
	var resp dnsIPCResponse
	switch strings.ToLower(strings.TrimSpace(req.Op)) {
	case "snapshot":
		resp.OK = true
		curVer := s.ring.Version()
		if req.Version > 0 && req.Version == curVer {
			resp.Version = curVer
		} else {
			resp.Entries, resp.Version = s.ring.Snapshot()
		}
	case "clear_log":
		resp.OK = true
		resp.Version = s.ring.Clear()
	case "clear_cache":
		if s.proxy == nil {
			resp.Error = "proxy unavailable"
		} else {
			s.proxy.ClearCache()
			resp.OK = true
		}
	case "set_upstream":
		if s.proxy == nil {
			resp.Error = "proxy unavailable"
		} else if len(req.Upstreams) == 0 {
			resp.Error = "no upstreams provided"
		} else if err := s.proxy.SetUpstreams(req.Upstreams); err != nil {
			resp.Error = err.Error()
		} else {
			resp.OK = true
		}
	default:
		resp.Error = fmt.Sprintf("unknown operation %q", req.Op)
	}
	resp.InstanceID = s.instanceID
	_ = writeIPC(f, resp)
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
	name, _ := windows.UTF16PtrFromString(sondnsPipeName)
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
	name, _ := windows.UTF16PtrFromString(sondnsPipeName)
	var h windows.Handle
	var err error
	deadline := time.Now().Add(400 * time.Millisecond)
	for {
		h, err = windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
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

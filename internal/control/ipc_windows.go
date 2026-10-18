//go:build windows

package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	appconfig "sondns/internal/config"
	"sondns/internal/requestlog"
)

const PipeName = `\\.\pipe\SonDNS\control`

type Request struct {
	Op         string               `json:"op"`
	InstanceID string               `json:"instance_id,omitempty"`
	Version    uint64               `json:"version,omitempty"`
	Upstreams  []appconfig.Upstream `json:"upstreams,omitempty"`
}

type Response struct {
	OK              bool               `json:"ok"`
	Unchanged       bool               `json:"unchanged,omitempty"`
	InstanceID      string             `json:"instance_id,omitempty"`
	SchemaVersion   int                `json:"schema_version,omitempty"`
	Version         uint64             `json:"version,omitempty"`
	OverwrittenRows uint64             `json:"overwritten_rows,omitempty"`
	Entries         []requestlog.Entry `json:"entries,omitempty"`
	Error           string             `json:"error,omitempty"`
	State           any                `json:"state,omitempty"`
}

type Engine interface {
	SetUpstreams(context.Context, []appconfig.Upstream) error
}

const (
	ipcMaxHandlers      = 8
	ipcMaxRequestBytes  = 64 * 1024
	ipcMaxResponseBytes = 4 * 1024 * 1024
	ipcIOTimeout        = 3 * time.Second
	// GENERIC_WRITE includes FILE_CREATE_PIPE_INSTANCE on a named pipe.
	// Clients need data I/O, never permission to create another server instance.
	ipcClientAccess = windows.FILE_READ_DATA | windows.FILE_WRITE_DATA | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE
	ipcClientFlags  = windows.FILE_FLAG_OVERLAPPED | windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION
)

type Server struct {
	PipeName   string
	instanceID string
	ring       *requestlog.Ring
	proxy      Engine
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	mu         sync.Mutex
	active     map[windows.Handle]*os.File
	ownerSID   string
	elevated   bool
}

func NewServer(ring *requestlog.Ring, proxy Engine) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{PipeName: PipeName, instanceID: fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()), ring: ring, proxy: proxy, ctx: ctx, cancel: cancel, done: make(chan struct{}), active: make(map[windows.Handle]*os.File)}
}

func (s *Server) Start() (err error) {
	defer func() {
		if err != nil {
			s.cancel()
			close(s.done)
		}
	}()
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("IPC server identity: %w", err)
	}
	s.ownerSID = user.User.Sid.String()
	s.elevated = token.IsElevated()
	acl := "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;0x100083;;;AU)"
	if !s.elevated {
		// An unelevated isolated preview belongs to its own user. Elevated
		// runtimes only grant server-creation rights to administrators/SYSTEM.
		acl += "(A;;GA;;;" + s.ownerSID + ")"
	}
	// The service runs at high/System integrity; normal desktop clients must
	// still be able to send read requests. Authorization gates mutations.
	acl += "S:(ML;;NW;;;ME)"
	sd, err := windows.SecurityDescriptorFromString(acl)
	if err != nil {
		return fmt.Errorf("IPC security descriptor: %w", err)
	}
	name, err := windows.UTF16PtrFromString(s.PipeName)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	first, err := CreateDNSPipe(name, sa, windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	if err != nil {
		return fmt.Errorf("reserve IPC pipe: %w", err)
	}
	var reservation windows.Handle
	process := windows.CurrentProcess()
	if err = windows.DuplicateHandle(process, first, process, &reservation, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = windows.CloseHandle(first)
		return fmt.Errorf("retain IPC reservation: %w", err)
	}
	go s.serve(first, reservation, name, sa)
	return nil
}

func CreateDNSPipe(name *uint16, sa *windows.SecurityAttributes, flags uint32) (windows.Handle, error) {
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, windows.PIPE_UNLIMITED_INSTANCES, 64*1024, 64*1024, 1000, sa)
}

func (s *Server) serve(first, reservation windows.Handle, name *uint16, sa *windows.SecurityAttributes) {
	var handlers sync.WaitGroup
	defer func() {
		if first != 0 {
			_ = windows.CloseHandle(first)
		}
		handlers.Wait()
		// Keep the first instance alive for the entire server lifetime, even
		// between clients, so another process cannot take over the pipe name.
		_ = windows.CloseHandle(reservation)
		close(s.done)
	}()
	slots := make(chan struct{}, ipcMaxHandlers)
	for {
		select {
		case slots <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		if s.ctx.Err() != nil {
			return
		}
		h := first
		first = 0
		var err error
		if h == 0 {
			h, err = CreateDNSPipe(name, sa, 0)
		}
		if err != nil {
			<-slots
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		s.mu.Lock()
		s.active[h] = nil
		s.mu.Unlock()
		err = s.accept(h)
		if err != nil || s.ctx.Err() != nil {
			s.mu.Lock()
			delete(s.active, h)
			s.mu.Unlock()
			_ = windows.CloseHandle(h)
			<-slots
			continue
		}
		f := os.NewFile(uintptr(h), "SonDNS IPC")
		if f == nil {
			s.mu.Lock()
			delete(s.active, h)
			s.mu.Unlock()
			_ = windows.CloseHandle(h)
			<-slots
			continue
		}
		s.mu.Lock()
		s.active[h] = f
		s.mu.Unlock()
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { s.mu.Lock(); delete(s.active, h); s.mu.Unlock(); _ = f.Close(); <-slots }()
			if s.ctx.Err() == nil {
				s.handle(f)
			}
		}()
	}
}

func (s *Server) accept(h windows.Handle) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(h, &overlapped)
	if err == nil || errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return err
	}
	for {
		if s.ctx.Err() != nil {
			// Complete the pending connect with a local peer instead of cancelling
			// an unconnected pipe: CancelIoEx can itself block in Windows here.
			name, nameErr := windows.UTF16PtrFromString(s.PipeName)
			if nameErr == nil {
				peer, peerErr := windows.CreateFile(name, ipcClientAccess, 0, nil, windows.OPEN_EXISTING, ipcClientFlags, 0)
				if peerErr == nil {
					_ = windows.CloseHandle(peer)
				}
			}
		}
		state, err := windows.WaitForSingleObject(event, 100)
		if err != nil {
			_ = windows.CancelIoEx(h, &overlapped)
			var n uint32
			_ = windows.GetOverlappedResult(h, &overlapped, &n, true)
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			var n uint32
			return windows.GetOverlappedResult(h, &overlapped, &n, false)
		}
	}
}

func (s *Server) handle(f *os.File) {
	if err := f.SetReadDeadline(time.Now().Add(ipcIOTimeout)); err != nil {
		return
	}
	// ReadSlice is bounded by the buffer; ReadBytes would grow without a bound.
	line, err := bufio.NewReaderSize(f, ipcMaxRequestBytes).ReadSlice('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	if len(line) == 0 {
		return
	}
	_ = f.SetReadDeadline(time.Time{})
	var req Request
	var resp Response
	if json.Unmarshal(line, &req) != nil {
		resp = Response{Error: "invalid request", SchemaVersion: 2}
	} else if ipcRequiresAuthorization(req.Op) && !s.authorizedFile(f) {
		resp = Response{Error: "Administrator permission is required for this operation", SchemaVersion: 2, InstanceID: s.instanceID}
	} else {
		resp = s.respond(req)
	}
	if err := f.SetWriteDeadline(time.Now().Add(ipcIOTimeout)); err == nil {
		_ = writeIPC(f, resp)
	}
}

func ipcRequiresAuthorization(op string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "set_upstream", "clear_log":
		return true
	default:
		return false
	}
}

var impersonateDNSPipeClient = windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")

func (s *Server) authorizedFile(f *os.File) bool {
	conn, err := f.SyscallConn()
	if err != nil {
		return false
	}
	authorized := false
	err = conn.Control(func(handle uintptr) { authorized = s.authorizedClient(windows.Handle(handle)) })
	return err == nil && authorized
}

func (s *Server) authorizedClient(pipe windows.Handle) bool {
	// Authenticate the token attached to the pipe, not a PID or data claimed
	// by the caller. Never run resolver/configuration code while impersonating.
	runtime.LockOSThread()
	impersonating := false
	defer func() {
		if impersonating {
			if err := windows.RevertToSelf(); err != nil {
				// A thread with an unknown identity must never return to Go's pool.
				panic(fmt.Sprintf("IPC RevertToSelf failed: %v", err))
			}
		}
		runtime.UnlockOSThread()
	}()
	ok, _, _ := impersonateDNSPipeClient.Call(uintptr(pipe))
	if ok == 0 {
		return false
	}
	impersonating = true
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		return true
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	admin, err := token.IsMember(admins)
	if err == nil && admin && token.IsElevated() {
		return true
	}
	return !s.elevated && user.User.Sid.String() == s.ownerSID
}

func (s *Server) respond(req Request) Response {
	resp := Response{SchemaVersion: 2}
	switch strings.ToLower(strings.TrimSpace(req.Op)) {
	case "state":
		if p, ok := s.proxy.(interface{ RuntimeState() any }); ok {
			resp.State = p.RuntimeState()
			resp.OK = true
		} else {
			resp.Error = "runtime state is unavailable"
		}
	case "snapshot":
		resp.OK = true
		curVer := s.ring.Version()
		if req.InstanceID == s.instanceID && req.Version == curVer {
			resp.Version = curVer
			resp.Unchanged = true
		} else {
			resp.Entries, resp.Version, resp.OverwrittenRows = s.ring.SnapshotStats()
		}
	case "clear_log":
		resp.OK = true
		resp.Version = s.ring.Clear()
	case "set_upstream":
		if s.proxy == nil {
			resp.Error = "proxy unavailable"
		} else if len(req.Upstreams) == 0 {
			resp.Error = "no upstreams provided"
		} else if err := s.proxy.SetUpstreams(s.ctx, req.Upstreams); err != nil {
			resp.Error = err.Error()
		} else {
			resp.OK = true
		}
	default:
		resp.Error = fmt.Sprintf("unknown operation %q", req.Op)
	}
	resp.InstanceID = s.instanceID
	return resp
}

func writeIPC(f *os.File, v Response) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

func (s *Server) Close() {
	s.cancel()
	s.mu.Lock()
	for _, f := range s.active {
		if f != nil {
			_ = f.Close()
		}
		// accept owns and wakes its OVERLAPPED connect after observing ctx.
		// Do not race its pending connect with another cancellation here.
	}
	s.mu.Unlock()
	<-s.done
}

// A rejection is distinct from a connection failure or an uncertain timeout.
type ResponseError struct{ Message string }

func (e *ResponseError) Error() string { return e.Message }

func Call(req Request) (Response, error) {
	return CallAt(PipeName, req)
}

func CallAt(pipeName string, req Request) (Response, error) {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return Response{}, err
	}
	var h windows.Handle
	deadline := time.Now().Add(1200 * time.Millisecond)
	for {
		h, err = windows.CreateFile(name, ipcClientAccess, 0, nil, windows.OPEN_EXISTING, ipcClientFlags, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || time.Now().After(deadline) {
			return Response{}, fmt.Errorf("open IPC pipe: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	f := os.NewFile(uintptr(h), "SonDNS IPC")
	if f == nil {
		windows.CloseHandle(h)
		return Response{}, errors.New("invalid IPC handle")
	}
	defer f.Close()
	// Connection timeout alone does not bound a stalled server's reply. An
	// overlapped pipe lets Go cancel I/O when this deadline expires.
	timeout := 3 * time.Second
	if strings.EqualFold(strings.TrimSpace(req.Op), "set_upstream") {
		timeout = 15 * time.Second
	}
	if err = f.SetDeadline(time.Now().Add(timeout)); err != nil {
		return Response{}, err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	b = append(b, '\n')
	if len(b) > ipcMaxRequestBytes {
		return Response{}, errors.New("IPC request exceeds size limit")
	}
	if _, err = f.Write(b); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(f, ipcMaxResponseBytes+1), 64*1024).ReadBytes('\n')
	if len(line) > ipcMaxResponseBytes {
		return Response{}, errors.New("IPC response exceeds size limit")
	}
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err = json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		return resp, &ResponseError{Message: resp.Error}
	}
	return resp, nil
}

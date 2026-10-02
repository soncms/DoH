//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	tcpTableOwnerPIDAll            = 5
	afInet                         = 2
	processQueryLimitedInformation = 0x1000
	tcpEstablished                 = 5
)

type tcpRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  [4]byte
	RemoteAddr uint32
	RemotePort [4]byte
	PID        uint32
}

type ConnectionMonitor struct {
	store     *ConnectionStore
	dns       *DNSRequestRing
	logger    interface{ Printf(string, ...interface{}) }
	stop      chan struct{}
	done      chan struct{}
	retention time.Duration
	mu        sync.Mutex
	active    map[string]ConnectionEntry
}

func NewConnectionMonitor(store *ConnectionStore, dns *DNSRequestRing, logger interface{ Printf(string, ...interface{}) }, retention time.Duration) *ConnectionMonitor {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	return &ConnectionMonitor{store: store, dns: dns, logger: logger, retention: retention, active: make(map[string]ConnectionEntry)}
}

func (m *ConnectionMonitor) Start() {
	if m == nil || m.store == nil {
		return
	}
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	go m.loop()
}

func (m *ConnectionMonitor) loop() {
	defer close(m.done)
	// A short interval catches short-lived TCP flows while keeping the monitor
	// independent from the WinDivert handle owned by winws2.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.sample()
		select {
		case <-m.stop:
			return
		case <-ticker.C:
		}
	}
}

func (m *ConnectionMonitor) sample() {
	rows, err := queryTCPRows()
	if err != nil {
		if m.logger != nil {
			m.logger.Printf("connection monitor: %v", err)
		}
		return
	}
	now := time.Now()
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.State != tcpEstablished {
			continue
		}
		id := fmt.Sprintf("%d:%s:%s:%d", row.PID, row.localAddrString(), row.remoteIPString(), row.remotePortValue())
		seen[id] = struct{}{}
		m.mu.Lock()
		e, ok := m.active[id]
		if !ok {
			e = ConnectionEntry{ID: id, FirstSeen: now, PID: row.PID, LocalAddr: row.localAddrString(),
				RemoteIP: row.remoteIPString(), RemotePort: row.remotePortValue(), Protocol: "TCP", State: "established", Decision: "allow"}
			e.ProcessPath = processImage(row.PID)
			e.Domain = m.domainForIP(e.RemoteIP)
		}
		e.LastSeen = now
		if e.Domain == "" {
			e.Domain = m.domainForIP(e.RemoteIP)
		}
		m.active[id] = e
		m.mu.Unlock()
		if err := m.store.Upsert(e); err != nil && m.logger != nil {
			m.logger.Printf("connection monitor store: %v", err)
		}
	}
	m.mu.Lock()
	for id, e := range m.active {
		if _, ok := seen[id]; !ok {
			e.State = "closed"
			e.LastSeen = now
			_ = m.store.Upsert(e)
			delete(m.active, id)
		}
	}
	m.mu.Unlock()
	_ = m.store.Prune(m.retention)
}

func (m *ConnectionMonitor) domainForIP(ip string) string {
	if m == nil || m.dns == nil || ip == "" {
		return ""
	}
	rows, _ := m.dns.Snapshot()
	for i := len(rows) - 1; i >= 0; i-- {
		for _, answer := range rows[i].AnswerIPs {
			if answer == ip {
				return rows[i].Domain
			}
		}
	}
	return ""
}

func (m *ConnectionMonitor) Recent(limit int) ([]ConnectionEntry, error) {
	if m == nil || m.store == nil {
		return nil, nil
	}
	return m.store.Recent(limit)
}

func (m *ConnectionMonitor) Shutdown() {
	if m == nil || m.stop == nil {
		return
	}
	close(m.stop)
	select {
	case <-m.done:
	case <-time.After(2 * time.Second):
	}
	m.stop = nil
}

func queryTCPRows() ([]tcpRowOwnerPID, error) {
	dll := windows.NewLazySystemDLL("iphlpapi.dll")
	proc := dll.NewProc("GetExtendedTcpTable")
	var size uint32
	ret, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0)
	if ret != 122 && ret != 0 {
		return nil, windows.Errno(ret)
	}
	buf := make([]byte, size)
	ret, _, _ = proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0)
	if ret != 0 {
		return nil, windows.Errno(ret)
	}
	if len(buf) < 4 {
		return nil, nil
	}
	count := *(*uint32)(unsafe.Pointer(&buf[0]))
	rowSize := int(unsafe.Sizeof(tcpRowOwnerPID{}))
	max := (len(buf) - 4) / rowSize
	if int(count) > max {
		count = uint32(max)
	}
	rows := make([]tcpRowOwnerPID, count)
	for i := range rows {
		rows[i] = *(*tcpRowOwnerPID)(unsafe.Pointer(&buf[4+i*rowSize]))
	}
	return rows, nil
}

func (r tcpRowOwnerPID) remoteIPString() string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, r.RemoteAddr)
	return net.IP(b).String()
}

func (r tcpRowOwnerPID) localAddrString() string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, r.LocalAddr)
	return net.JoinHostPort(net.IP(b).String(), fmt.Sprintf("%d", binary.BigEndian.Uint16(r.LocalPort[0:2])))
}

func (r tcpRowOwnerPID) remotePortValue() uint16 { return binary.BigEndian.Uint16(r.RemotePort[0:2]) }

func processImage(pid uint32) string {
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(windows.UTF16ToString(buf[:size]))))
}

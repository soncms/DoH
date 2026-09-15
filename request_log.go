package main

import (
	"sync"
	"time"
)

const maxDNSLogRows = 200

// DNSRequestEntry is the in-memory request contract shared by the engine and
// the GUI IPC client. No raw packet, client address, or upstream URL is kept.
type DNSRequestEntry struct {
	Time      time.Time `json:"ts"`
	Domain    string    `json:"domain"`
	Type      string    `json:"type"`
	Result    string    `json:"result"`
	LatencyMS int64     `json:"latency_ms"`
	AnswerIPs []string  `json:"answer_ips,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// DNSRequestRing stores only the latest requests in process memory.
type DNSRequestRing struct {
	mu      sync.RWMutex
	rows    []DNSRequestEntry
	version uint64
}

func NewDNSRequestRing() *DNSRequestRing {
	return &DNSRequestRing{rows: make([]DNSRequestEntry, 0, maxDNSLogRows)}
}

func (r *DNSRequestRing) Add(entry DNSRequestEntry) {
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	if entry.LatencyMS < 0 {
		entry.LatencyMS = 0
	}
	entry.AnswerIPs = append([]string(nil), entry.AnswerIPs...)
	r.mu.Lock()
	if len(r.rows) < maxDNSLogRows {
		r.rows = append(r.rows, entry)
	} else {
		copy(r.rows, r.rows[1:])
		r.rows[maxDNSLogRows-1] = entry
	}
	r.version++
	r.mu.Unlock()
}

func (r *DNSRequestRing) Snapshot() ([]DNSRequestEntry, uint64) {
	r.mu.RLock()
	rows := append([]DNSRequestEntry(nil), r.rows...)
	for i := range rows {
		rows[i].AnswerIPs = append([]string(nil), rows[i].AnswerIPs...)
	}
	v := r.version
	r.mu.RUnlock()
	return rows, v
}

func (r *DNSRequestRing) Version() uint64 {
	r.mu.RLock()
	v := r.version
	r.mu.RUnlock()
	return v
}

func (r *DNSRequestRing) Clear() uint64 {
	r.mu.Lock()
	r.rows = r.rows[:0]
	r.version++
	v := r.version
	r.mu.Unlock()
	return v
}

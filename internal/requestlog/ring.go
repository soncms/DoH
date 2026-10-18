package requestlog

import (
	"sync"
	"time"
)

const Capacity = 200

// DNSAttempt records one engine attempt. A client request has exactly one
// DNSRequestEntry even when the engine retries it.
type Attempt struct {
	Number     int      `json:"number"`
	DurationUS int64    `json:"duration_us"`
	Upstreams  []string `json:"upstreams,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// DNSRequestEntry is the in-memory request contract shared by the engine and
// the GUI IPC client. No raw packet or client address is kept.
type Entry struct {
	ID            uint64    `json:"id"`
	Time          time.Time `json:"ts"`
	CompletedTime time.Time `json:"completed_ts"`
	Domain        string    `json:"domain"`
	Type          string    `json:"type"`
	Protocol      string    `json:"protocol,omitempty"`
	Result        string    `json:"result"`
	RCode         string    `json:"rcode,omitempty"`
	LatencyMS     int64     `json:"latency_ms"` // Kept for IPC compatibility.
	DurationUS    int64     `json:"duration_us"`
	Upstream      string    `json:"upstream,omitempty"`
	AttemptCount  int       `json:"attempt_count"`
	Attempts      []Attempt `json:"attempts,omitempty"`
	AnswerIPs     []string  `json:"answer_ips,omitempty"`
	Error         string    `json:"error,omitempty"`
	DeliveryError string    `json:"delivery_error,omitempty"`
}

// DNSRequestRing stores only the latest requests in process memory.
type Ring struct {
	mu          sync.RWMutex
	rows        []Entry
	start       int
	version     uint64
	nextID      uint64
	overwritten uint64
}

func New() *Ring {
	return &Ring{rows: make([]Entry, 0, Capacity)}
}

func (r *Ring) Add(entry Entry) {
	now := time.Now()
	if entry.Time.IsZero() {
		entry.Time = now
	}
	if entry.CompletedTime.IsZero() {
		entry.CompletedTime = now
	}
	if entry.DurationUS < 0 {
		entry.DurationUS = 0
	}
	if entry.LatencyMS < 0 {
		entry.LatencyMS = 0
	}
	if entry.DurationUS == 0 && entry.LatencyMS > 0 {
		entry.DurationUS = entry.LatencyMS * 1000
	}
	if entry.LatencyMS == 0 && entry.DurationUS > 0 {
		entry.LatencyMS = entry.DurationUS / 1000
	}
	entry = cloneDNSRequestEntry(entry)
	r.mu.Lock()
	r.nextID++
	entry.ID = r.nextID
	if len(r.rows) < Capacity {
		r.rows = append(r.rows, entry)
	} else {
		r.rows[r.start] = entry
		r.start = (r.start + 1) % Capacity
		r.overwritten++
	}
	r.version++
	r.mu.Unlock()
}

func cloneDNSRequestEntry(entry Entry) Entry {
	entry.AnswerIPs = append([]string(nil), entry.AnswerIPs...)
	entry.Attempts = append([]Attempt(nil), entry.Attempts...)
	for i := range entry.Attempts {
		entry.Attempts[i].Upstreams = append([]string(nil), entry.Attempts[i].Upstreams...)
	}
	return entry
}

func (r *Ring) Snapshot() ([]Entry, uint64) {
	rows, version, _ := r.SnapshotStats()
	return rows, version
}

func (r *Ring) SnapshotStats() ([]Entry, uint64, uint64) {
	r.mu.RLock()
	rows := make([]Entry, len(r.rows))
	for i := range rows {
		rows[i] = cloneDNSRequestEntry(r.rows[(r.start+i)%len(r.rows)])
	}
	v, overwritten := r.version, r.overwritten
	r.mu.RUnlock()
	return rows, v, overwritten
}

func (r *Ring) Version() uint64 {
	r.mu.RLock()
	v := r.version
	r.mu.RUnlock()
	return v
}

func (r *Ring) Clear() uint64 {
	r.mu.Lock()
	clear(r.rows)
	r.rows = r.rows[:0]
	r.start = 0
	r.version++
	v := r.version
	r.mu.Unlock()
	return v
}

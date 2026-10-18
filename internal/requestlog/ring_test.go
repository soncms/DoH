package requestlog

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDNSRequestRingBoundedAndOrdered(t *testing.T) {
	r := New()
	for i := 0; i < 220; i++ {
		r.Add(Entry{Time: time.Unix(int64(i), 0), Domain: fmt.Sprintf("host-%03d.example.", i), Type: "A", Result: "NOERROR", LatencyMS: int64(i), AnswerIPs: []string{"192.0.2.1"}})
	}
	rows, v := r.Snapshot()
	if len(rows) != 200 || rows[0].Domain != "host-020.example." || rows[199].Domain != "host-219.example." || v != 220 {
		t.Fatalf("bad ring: %d %d %+v", len(rows), v, rows[0])
	}
}

func TestDNSRequestRingConcurrentAndClear(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Add(Entry{Domain: fmt.Sprintf("%d.example.", i), Type: "AAAA", Result: "NOERROR"})
		}(i)
	}
	wg.Wait()
	rows, v := r.Snapshot()
	if len(rows) != 50 || v != 50 {
		t.Fatalf("lost rows: %d version %d", len(rows), v)
	}
	cv := r.Clear()
	rows, v = r.Snapshot()
	if len(rows) != 0 || v != cv || cv <= 50 {
		t.Fatalf("clear failed: %d %d %d", len(rows), v, cv)
	}
}

func TestDNSRequestRingClonesAnswerIPs(t *testing.T) {
	r := New()
	ips := []string{"192.0.2.1"}
	r.Add(Entry{Domain: "a.", Type: "A", Result: "NOERROR", AnswerIPs: ips})
	ips[0] = "bad"
	rows, _ := r.Snapshot()
	if rows[0].AnswerIPs[0] != "192.0.2.1" {
		t.Fatal("ring retained caller slice")
	}
	rows[0].AnswerIPs[0] = "mutated snapshot"
	rows, _ = r.Snapshot()
	if rows[0].AnswerIPs[0] != "192.0.2.1" {
		t.Fatal("snapshot modified the ring")
	}
}

func TestDNSRequestRingWrapThenClear(t *testing.T) {
	r := New()
	for i := 0; i < 3*Capacity+7; i++ {
		r.Add(Entry{Domain: fmt.Sprint(i)})
	}
	r.Clear()
	r.Add(Entry{Domain: "fresh"})
	rows, _ := r.Snapshot()
	if len(rows) != 1 || rows[0].Domain != "fresh" {
		t.Fatalf("clear failed after wrap: %+v", rows)
	}
}

func BenchmarkDNSRequestRingAddAtCapacity(b *testing.B) {
	r := New()
	entry := Entry{Time: time.Unix(1, 0), Domain: "example.com", Type: "A", Result: "NOERROR"}
	for i := 0; i < Capacity; i++ {
		r.Add(entry)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Add(entry)
	}
}

func TestDNSRequestRingIDsOverwriteStatsAndDeepCloneAttempts(t *testing.T) {
	r := New()
	attempts := []Attempt{{Number: 1, Upstreams: []string{"https://dns.example/dns-query"}}}
	for i := 0; i < Capacity+3; i++ {
		r.Add(Entry{Domain: fmt.Sprint(i), Attempts: attempts})
	}
	attempts[0].Upstreams[0] = "mutated"
	rows, version, overwritten := r.SnapshotStats()
	if len(rows) != Capacity || version != Capacity+3 || overwritten != 3 {
		t.Fatalf("rows=%d version=%d overwritten=%d", len(rows), version, overwritten)
	}
	if rows[0].ID != 4 || rows[len(rows)-1].ID != Capacity+3 {
		t.Fatalf("unexpected retained ID range: %d..%d", rows[0].ID, rows[len(rows)-1].ID)
	}
	if rows[0].Attempts[0].Upstreams[0] == "mutated" {
		t.Fatal("ring retained nested caller slice")
	}
	rows[0].Attempts[0].Upstreams[0] = "snapshot mutation"
	again, _, _ := r.SnapshotStats()
	if again[0].Attempts[0].Upstreams[0] == "snapshot mutation" {
		t.Fatal("snapshot exposed nested ring storage")
	}
}

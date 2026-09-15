package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDNSRequestRingBoundedAndOrdered(t *testing.T) {
	r := NewDNSRequestRing()
	for i := 0; i < 220; i++ {
		r.Add(DNSRequestEntry{Time: time.Unix(int64(i), 0), Domain: fmt.Sprintf("host-%03d.example.", i), Type: "A", Result: "NOERROR", LatencyMS: int64(i), AnswerIPs: []string{"192.0.2.1"}})
	}
	rows, v := r.Snapshot()
	if len(rows) != 200 || rows[0].Domain != "host-020.example." || rows[199].Domain != "host-219.example." || v != 220 {
		t.Fatalf("bad ring: %d %d %+v", len(rows), v, rows[0])
	}
}
func TestDNSRequestRingConcurrentAndClear(t *testing.T) {
	r := NewDNSRequestRing()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Add(DNSRequestEntry{Domain: fmt.Sprintf("%d.example.", i), Type: "AAAA", Result: "NOERROR"})
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
	r := NewDNSRequestRing()
	ips := []string{"192.0.2.1"}
	r.Add(DNSRequestEntry{Domain: "a.", Type: "A", Result: "NOERROR", AnswerIPs: ips})
	ips[0] = "bad"
	rows, _ := r.Snapshot()
	if rows[0].AnswerIPs[0] != "192.0.2.1" {
		t.Fatal("ring retained caller slice")
	}
}

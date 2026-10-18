package main

import (
	"context"
	"testing"
	"time"
)

func TestPendingSwapStopsNewQueriesUntilOldQueryDrains(t *testing.T) {
	var lock upstreamMutex
	lock.RLock()
	writerDone := make(chan struct{})
	go func() { _ = lock.LockContext(context.Background()); close(writerDone) }()
	// Wait for the writer to own admission, rather than relying on scheduling.
	deadline := time.Now().Add(time.Second)
	for len(lock.admission) != 0 {
		if time.Now().After(deadline) {
			lock.RUnlock()
			t.Fatal("writer did not enter admission")
		}
		time.Sleep(time.Millisecond)
	}
	readerDone := make(chan struct{})
	go func() { lock.RLock(); lock.RUnlock(); close(readerDone) }()
	select {
	case <-readerDone:
		lock.RUnlock()
		t.Fatal("new query bypassed a pending swap")
	case <-time.After(20 * time.Millisecond):
	}
	lock.RUnlock()
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("swap starved")
	}
	select {
	case <-readerDone:
		t.Fatal("query bypassed an active swap")
	default:
	}
	lock.Unlock()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("swap did not release queued query")
	}
}

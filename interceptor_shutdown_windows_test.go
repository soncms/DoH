//go:build windows

package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

type interceptorTestProc func(...uintptr) (uintptr, uintptr, error)

func (p interceptorTestProc) Call(args ...uintptr) (uintptr, uintptr, error) { return p(args...) }

func shutdownFixture(t *testing.T) *Interceptor {
	t.Helper()
	// A real, otherwise unused event makes the cancellation fallback harmless.
	// No test opens WinDivert, installs a driver or changes the active service.
	h, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Interceptor{handle: h, ctx: ctx, cancel: cancel, log: log.New(io.Discard, "", 0)}
}

func awaitInterceptorSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for interceptor fixture")
	}
}

func TestInterceptorShutdownUnblocksIdleWorkerBeforeClose(t *testing.T) {
	i := shutdownFixture(t)
	entered, stop, recvLeft := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var shutdowns, closes atomic.Int32
	i.shutdown = interceptorTestProc(func(args ...uintptr) (uintptr, uintptr, error) {
		shutdowns.Add(1)
		if len(args) != 2 || args[0] != uintptr(i.handle) || args[1] != winDivertShutdownBoth || i.ctx.Err() == nil {
			t.Error("shutdown did not cancel and detach the intended handle")
		}
		close(stop)
		return 1, 0, nil
	})
	i.close = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) {
		closes.Add(1)
		select {
		case <-recvLeft:
		default:
			t.Error("handle closed while receive was still in flight")
		}
		return 1, 0, nil
	})
	// Model a blocked native receive. Buffer syscalls retain their concrete
	// LazyProc pointer annotations and are exercised on the clean test VM.
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		close(entered)
		<-stop
		close(recvLeft)
	}()
	awaitInterceptorSignal(t, entered)
	i.Shutdown()
	i.Shutdown()
	if shutdowns.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("shutdown/close must occur once: %d/%d", shutdowns.Load(), closes.Load())
	}
}

func TestInterceptorShutdownWaitsForPacketWorker(t *testing.T) {
	i := shutdownFixture(t)
	detached, closeEntered, workerReleased := make(chan struct{}), make(chan struct{}), make(chan struct{})
	i.wg.Add(1) // A send/answer that was already in flight when shutdown began.
	var release sync.Once
	releaseWorker := func() { release.Do(func() { close(workerReleased); i.wg.Done() }) }
	t.Cleanup(releaseWorker)
	i.shutdown = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) {
		close(detached)
		return 1, 0, nil
	})
	i.close = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) {
		close(closeEntered)
		select {
		case <-workerReleased:
		default:
			t.Error("handle was recycled before the packet worker finished")
		}
		return 1, 0, nil
	})
	done := make(chan struct{})
	go func() { i.Shutdown(); close(done) }()
	awaitInterceptorSignal(t, detached)
	select {
	case <-closeEntered:
		t.Fatal("shutdown closed an active worker's handle")
	case <-time.After(30 * time.Millisecond):
	}
	releaseWorker()
	awaitInterceptorSignal(t, done)
	awaitInterceptorSignal(t, closeEntered)
}

func TestInterceptorShutdownTimeoutRetainsHandleUntilWorkerExits(t *testing.T) {
	i := shutdownFixture(t)
	var messages bytes.Buffer
	i.log = log.New(&messages, "", 0)
	closed := make(chan struct{})
	i.wg.Add(1)
	var release sync.Once
	releaseWorker := func() { release.Do(i.wg.Done) }
	t.Cleanup(releaseWorker)
	i.shutdown = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { return 1, 0, nil })
	i.close = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { close(closed); return 1, 0, nil })
	i.Shutdown()
	i.Shutdown()
	select {
	case <-closed:
		t.Fatal("bounded shutdown wait recycled a handle that still had a worker")
	default:
	}
	if !strings.Contains(messages.String(), "handle will close after all packet workers finish") {
		t.Fatalf("slow shutdown was not reported: %s", messages.String())
	}
	releaseWorker()
	awaitInterceptorSignal(t, closed)
}

func TestInterceptorConcurrentShutdownIsIdempotent(t *testing.T) {
	i := shutdownFixture(t)
	var shutdowns, closes atomic.Int32
	i.shutdown = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { shutdowns.Add(1); return 1, 0, nil })
	i.close = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { closes.Add(1); return 1, 0, nil })
	var callers sync.WaitGroup
	for n := 0; n < 32; n++ {
		callers.Go(i.Shutdown)
	}
	callers.Wait()
	if shutdowns.Load() != 1 || closes.Load() != 1 {
		t.Fatalf("concurrent shutdown/close occurred %d/%d times", shutdowns.Load(), closes.Load())
	}
}

func TestInterceptorShutdownReportsNativeFailures(t *testing.T) {
	i := shutdownFixture(t)
	var messages bytes.Buffer
	i.log = log.New(&messages, "", 0)
	i.shutdown = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { return 0, 0, windows.ERROR_ACCESS_DENIED })
	i.close = interceptorTestProc(func(...uintptr) (uintptr, uintptr, error) { return 0, 0, windows.ERROR_INVALID_HANDLE })
	i.Shutdown()
	for _, want := range []string{"WinDivert shutdown failed:", "close WinDivert handle:"} {
		if !strings.Contains(messages.String(), want) {
			t.Fatalf("native failure missing from log: %s", messages.String())
		}
	}
}

func TestInterceptorShutdownWithoutOpenedHandle(t *testing.T) {
	var i *Interceptor
	i.Shutdown()
	i = &Interceptor{}
	i.Shutdown()
	i.Shutdown()
}

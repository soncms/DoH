package engine

import (
	"context"
	"sync"
	"time"
)

// A writer holds admission while existing queries drain. New queries cannot
// continuously win a TryLock race and starve a hot-swap under sustained load.
// Unlike a blocking RWMutex.Lock, waiting for that drain remains cancellable.
type UpstreamMutex struct {
	once      sync.Once
	admission chan struct{}
	queries   sync.RWMutex
}

func (m *UpstreamMutex) init() {
	m.once.Do(func() { m.admission = make(chan struct{}, 1); m.admission <- struct{}{} })
}

func (m *UpstreamMutex) RLock() {
	m.init()
	<-m.admission
	m.queries.RLock()
	m.admission <- struct{}{}
}

func (m *UpstreamMutex) RUnlock() { m.queries.RUnlock() }

func (m *UpstreamMutex) LockContext(ctx context.Context) error {
	m.init()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.admission:
	}
	for {
		if err := ctx.Err(); err != nil {
			m.admission <- struct{}{}
			return err
		}
		if m.queries.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			m.admission <- struct{}{}
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (m *UpstreamMutex) Unlock() {
	m.queries.Unlock()
	m.admission <- struct{}{}
}

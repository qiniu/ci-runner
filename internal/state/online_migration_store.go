package state

import (
	"context"
	"fmt"
	"sync"
)

//go:generate go run ./internal/storeproxygen

// The facade never embeds Store: every operation, including void log methods
// and audited callbacks, must pass through pin. A newly added Store method
// fails compilation until go generate supplies its forwarding implementation.
// The underlying Store passed to a transaction callback remains bound to that
// whole transaction; callers must not retain it after the callback returns.
type onlineMigrationStore struct {
	mu      sync.Mutex
	current Store
	epoch   int64
	active  int
	barrier *onlineMigrationBarrier
	failure error
}

var _ Store = (*onlineMigrationStore)(nil)

func newOnlineMigrationStore(initial Store, epoch int64) (*onlineMigrationStore, error) {
	if initial == nil || epoch <= 0 {
		return nil, fmt.Errorf("%w: invalid initial Store authority", errOnlineMigrationNotReady)
	}
	return &onlineMigrationStore{current: initial, epoch: epoch}, nil
}

func (s *onlineMigrationStore) pin() (Store, func(), error) {
	s.mu.Lock()
	for s.barrier != nil {
		barrier := s.barrier
		barrier.waiting++
		if barrier.waiting > barrier.queueBudget {
			// Saturation cancels preparation. Operations, including void log
			// appends, wait for safe cancellation rather than being dropped.
			// All waiters share one broadcast channel, not a payload queue.
			barrier.cancel()
		}
		s.mu.Unlock()
		<-barrier.resume
		s.mu.Lock()
		barrier.waiting--
	}
	if s.failure != nil {
		err := s.failure
		s.mu.Unlock()
		return nil, nil, err
	}
	store := s.current
	s.active++
	s.mu.Unlock()
	return store, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.active--
		if s.active == 0 && s.barrier != nil && !s.barrier.isDrained {
			s.barrier.isDrained = true
			close(s.barrier.drained)
		}
	}, nil
}

type onlineMigrationBarrier struct {
	store       *onlineMigrationStore
	source      Store
	epoch       int64
	ctx         context.Context
	cancel      context.CancelFunc
	drained     chan struct{}
	resume      chan struct{}
	queueBudget int
	waiting     int
	isDrained   bool
}

// drain first stops admissions, then waits for entire old Store operations to
// return. Before it returns a guard, cancellation can safely reopen the source.
// Afterward the authority controller owns bounded preparation and must restore
// roles before aborting, or finish the new authority after durable commit.
func (s *onlineMigrationStore) drain(ctx context.Context, queueBudget int) (*onlineMigrationBarrier, error) {
	if _, bounded := ctx.Deadline(); !bounded || queueBudget < 1 {
		return nil, fmt.Errorf("%w: barrier requires time and queue budgets", errOnlineMigrationNotReady)
	}
	s.mu.Lock()
	if s.failure != nil {
		err := s.failure
		s.mu.Unlock()
		return nil, err
	}
	if s.barrier != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("%w: another operation barrier is active", errOnlineMigrationNotReady)
	}
	barrierCtx, cancel := context.WithCancel(ctx)
	barrier := &onlineMigrationBarrier{
		store: s, source: s.current, epoch: s.epoch, ctx: barrierCtx, cancel: cancel,
		drained: make(chan struct{}), resume: make(chan struct{}), queueBudget: queueBudget,
	}
	s.barrier = barrier
	if s.active == 0 {
		barrier.isDrained = true
		close(barrier.drained)
	}
	s.mu.Unlock()
	select {
	case <-barrier.drained:
	case <-barrierCtx.Done():
	}
	if err := barrierCtx.Err(); err != nil {
		barrier.abort()
		return nil, err
	}
	return barrier, nil
}

// An unavailable/uncertain authority must fail queued calls explicitly rather
// than hanging them indefinitely or letting them write to an old primary.
func (b *onlineMigrationBarrier) fail() {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.barrier == b {
		s.failure = fmt.Errorf("%w: migration authority recovery is required", errOnlineMigrationNotReady)
		s.barrier = nil
		close(b.resume)
		b.cancel()
	}
}

// abort is safe only before authority commit and after any preparatory role
// fencing has been restored. The controller must not defer it unconditionally.
func (b *onlineMigrationBarrier) abort() {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.barrier == b {
		s.barrier = nil
		close(b.resume)
		b.cancel()
	}
}

// publish is private protocol plumbing, not authority to switch databases. The
// controller may call it only after durable authority commit and role recovery.
// A deadline that expires after that commit cannot authorize return to source.
func (b *onlineMigrationBarrier) publish(primary Store, epoch int64) error {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.barrier != b || !b.isDrained || s.active != 0 || primary == nil || epoch <= b.epoch {
		return fmt.Errorf("%w: stale or undrained Store barrier", errOnlineMigrationNotReady)
	}
	s.current, s.epoch, s.barrier = primary, epoch, nil
	close(b.resume)
	b.cancel()
	return nil
}

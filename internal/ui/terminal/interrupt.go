// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// InterruptAction describes the deterministic response to Ctrl+C. The caller
// owns the actual model/tool cancellation and process exit operations.
type InterruptAction uint8

const (
	InterruptCancelCurrent InterruptAction = iota + 1
	InterruptExitProcess
)

// InterruptState implements "first cancels, second exits" without sleeping.
// A signal outside the window starts a new cancellation cycle.
type InterruptState struct {
	mu     sync.Mutex
	window time.Duration
	armed  bool
	last   time.Time
}

// HandleInterrupts consumes process interrupts until the owner context ends.
// The callbacks keep process policy outside this package and make the behavior
// deterministic under scripted tests. A second interrupt ends the loop after
// invoking exitProcess; production callers normally terminate from there.
func HandleInterrupts(
	ctx context.Context,
	signals <-chan os.Signal,
	state *InterruptState,
	now func() time.Time,
	cancelCurrent func(),
	exitProcess func(),
) error {
	if signals == nil {
		return errors.New("interrupt signal channel is required")
	}
	if state == nil {
		return errors.New("interrupt state is required")
	}
	if now == nil {
		return errors.New("interrupt clock is required")
	}
	if cancelCurrent == nil || exitProcess == nil {
		return errors.New("interrupt callbacks are required")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-signals:
			if !ok {
				return nil
			}
			switch state.Handle(now()) {
			case InterruptCancelCurrent:
				cancelCurrent()
			case InterruptExitProcess:
				exitProcess()
				return nil
			}
		}
	}
}

func NewInterruptState(window time.Duration) (*InterruptState, error) {
	if window <= 0 {
		return nil, errors.New("interrupt window must be positive")
	}
	return &InterruptState{window: window}, nil
}

// Handle records a signal at the supplied time and returns the required action.
func (s *InterruptState) Handle(at time.Time) InterruptAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed {
		elapsed := at.Sub(s.last)
		if elapsed >= 0 && elapsed <= s.window {
			s.armed = false
			return InterruptExitProcess
		}
	}
	s.armed = true
	s.last = at
	return InterruptCancelCurrent
}

// Reset clears a pending second-interrupt window after an operation completes.
func (s *InterruptState) Reset() {
	s.mu.Lock()
	s.armed = false
	s.last = time.Time{}
	s.mu.Unlock()
}

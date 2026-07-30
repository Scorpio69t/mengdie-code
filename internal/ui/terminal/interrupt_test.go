// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestInterruptStateFirstCancelsSecondExits(t *testing.T) {
	state, err := NewInterruptState(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	if got := state.Handle(start); got != InterruptCancelCurrent {
		t.Fatalf("first Handle() = %v", got)
	}
	if got := state.Handle(start.Add(time.Second)); got != InterruptExitProcess {
		t.Fatalf("second Handle() = %v", got)
	}
	if got := state.Handle(start.Add(1500 * time.Millisecond)); got != InterruptCancelCurrent {
		t.Fatalf("new cycle Handle() = %v", got)
	}
}

func TestInterruptStateExpiredWindowCancelsAgain(t *testing.T) {
	state, err := NewInterruptState(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	state.Handle(start)
	if got := state.Handle(start.Add(2*time.Second + time.Nanosecond)); got != InterruptCancelCurrent {
		t.Fatalf("expired Handle() = %v", got)
	}
	if got := state.Handle(start.Add(3 * time.Second)); got != InterruptExitProcess {
		t.Fatalf("second cycle Handle() = %v", got)
	}
}

func TestInterruptStateResetAndBackwardClock(t *testing.T) {
	state, err := NewInterruptState(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	state.Handle(start)
	state.Reset()
	if got := state.Handle(start.Add(time.Second)); got != InterruptCancelCurrent {
		t.Fatalf("Handle() after Reset = %v", got)
	}
	if got := state.Handle(start); got != InterruptCancelCurrent {
		t.Fatalf("Handle() with backward clock = %v", got)
	}
}

func TestInterruptStateRejectsInvalidWindow(t *testing.T) {
	if _, err := NewInterruptState(0); err == nil {
		t.Fatal("NewInterruptState() accepted zero window")
	}
}

func TestHandleInterruptsUsesScriptedSignals(t *testing.T) {
	state, err := NewInterruptState(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 2)
	signals <- os.Interrupt
	signals <- os.Interrupt
	close(signals)
	start := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var ticks atomic.Int64
	var cancels atomic.Int64
	var exits atomic.Int64

	err = HandleInterrupts(
		context.Background(),
		signals,
		state,
		func() time.Time { return start.Add(time.Duration(ticks.Add(1)-1) * time.Second) },
		func() { cancels.Add(1) },
		func() { exits.Add(1) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancels.Load() != 1 || exits.Load() != 1 {
		t.Fatalf("callbacks = cancel:%d exit:%d", cancels.Load(), exits.Load())
	}
}

func TestHandleInterruptsValidatesDependenciesAndCancellation(t *testing.T) {
	state, err := NewInterruptState(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := HandleInterrupts(context.Background(), nil, state, time.Now, func() {}, func() {}); err == nil {
		t.Fatal("HandleInterrupts() accepted nil signal channel")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := HandleInterrupts(cancelled, make(chan os.Signal), state, time.Now, func() {}, func() {}); err != context.Canceled {
		t.Fatalf("HandleInterrupts() error = %v", err)
	}
}

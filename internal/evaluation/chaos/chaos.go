// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package chaos provides deterministic fault injection decorators used by
// P2-08B evaluation scenarios. It wraps production interfaces (events.Sink,
// provider.Provider, policy.Broker, tools.Registry, tools.MutationJournal,
// session.PublicFactBus) without modifying production code paths.
//
// Every chaos wrapper holds a *Controller; the Controller owns the schedule
// of planned fires and records an Observation per consumed fire. Tests
// inspect Observations() after the run to verify which kill points actually
// fired and what side-effect state was visible at that moment.
package chaos

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Hook identifies a single fault-injection point. Hook names mirror the
// recovery flow they validate so future scenario authors can read a schedule
// without consulting source.
type Hook string

const (
	HookEventStoreCommit     Hook = "event_store.commit"
	HookContextSummary       Hook = "agent.context_summary"
	HookPendingApproval      Hook = "policy.pending_approval"
	HookReadToolPre          Hook = "tools.read.pre"
	HookReadToolPost         Hook = "tools.read.post"
	HookPatchJournalPre      Hook = "patch.pre"
	HookPatchJournalPost     Hook = "patch.post"
	HookPatchJournalConflict Hook = "patch.conflict"
	HookTUIFactGap           Hook = "tui.fact_gap"
)

// FireKind tells the wrapper what to do when the schedule matches.
type FireKind string

const (
	// FireAbort causes the wrapped call to fail with a deterministic error.
	// The caller treats it like any other failure; recovery is exercised by
	// the resumed run.
	FireAbort FireKind = "abort"
	// FireContext cancels the call's context before delegating to the inner
	// implementation; the wrap surfaces context.Canceled so the runtime maps
	// it to run.cancelled instead of run.failed.
	FireContext FireKind = "context"
	// FireUnknown returns success but tags the observation as unknown_state.
	// Tests assert that the post-run view is internally consistent even when
	// the client never received an explicit ack.
	FireUnknown FireKind = "unknown"
)

// PlannedFire is one scheduled kill. AfterSeq applies only to
// HookEventStoreCommit; for every other hook AfterSeq is ignored and the
// fire is consumed on the first matching call.
type PlannedFire struct {
	Hook     Hook
	FireKind FireKind
	AfterSeq uint64
}

// Schedule is the deterministic input to a Controller. Seeds are reserved
// for future per-call jitter; today the controller fires in slice order.
type Schedule struct {
	Seed  int64
	Fires []PlannedFire
}

// Observation is one consumed fire, recorded exactly once per wrapper hit.
type Observation struct {
	Hook       Hook
	Fire       FireKind
	Seq        uint64
	Tool       string
	SideEffect bool
}

// Decision is the action returned by Controller.MaybeFire. Err is non-nil
// only for FireAbort and FireContext. Observed is true whenever a schedule
// entry (or an armed hook) was consumed by the call.
type Decision struct {
	Fire     FireKind
	Err      error
	Observed bool
}

// Controller is concurrency-safe. One Controller is shared by every chaos
// wrapper that participates in a single scenario run.
type Controller struct {
	mu sync.Mutex

	schedule    []PlannedFire
	original    []PlannedFire
	fired       []Observation
	armed       map[Hook]FireKind
	scheduleKey int64
}

// New validates the schedule and constructs a Controller. Unknown hook
// names or fire kinds produce a typed error so scenario JSON can be
// rejected before any wrapper is wired.
func New(schedule Schedule) (*Controller, error) {
	fires := make([]PlannedFire, 0, len(schedule.Fires))
	for index, planned := range schedule.Fires {
		normalized, err := normalizePlannedFire(planned)
		if err != nil {
			return nil, fmt.Errorf("chaos: fire %d: %w", index, err)
		}
		fires = append(fires, normalized)
	}
	return &Controller{
		schedule:    fires,
		original:    append([]PlannedFire(nil), fires...),
		armed:       make(map[Hook]FireKind),
		scheduleKey: schedule.Seed,
	}, nil
}

func normalizePlannedFire(planned PlannedFire) (PlannedFire, error) {
	hook := Hook(strings.TrimSpace(string(planned.Hook)))
	if hook == "" {
		return PlannedFire{}, errors.New("hook is required")
	}
	if _, ok := knownHooks[hook]; !ok {
		return PlannedFire{}, fmt.Errorf("unknown hook %q", hook)
	}
	kind := FireKind(strings.TrimSpace(string(planned.FireKind)))
	if kind == "" {
		return PlannedFire{}, errors.New("fire kind is required")
	}
	if _, ok := knownFireKinds[kind]; !ok {
		return PlannedFire{}, fmt.Errorf("unknown fire kind %q", kind)
	}
	return PlannedFire{Hook: hook, FireKind: kind, AfterSeq: planned.AfterSeq}, nil
}

var knownHooks = map[Hook]struct{}{
	HookEventStoreCommit:     {},
	HookContextSummary:       {},
	HookPendingApproval:      {},
	HookReadToolPre:          {},
	HookReadToolPost:         {},
	HookPatchJournalPre:      {},
	HookPatchJournalPost:     {},
	HookPatchJournalConflict: {},
	HookTUIFactGap:           {},
}

var knownFireKinds = map[FireKind]struct{}{
	FireAbort:   {},
	FireContext: {},
	FireUnknown: {},
}

// ErrChaosContextCanceled is the sentinel error returned for FireContext so
// tests can match it without importing context themselves.
var ErrChaosContextCanceled = errors.New("chaos: context canceled")

// MaybeFire consults the schedule and the armed map for the given hook.
// seq is the current EventStore sequence (0 when the wrapper has no seq).
// tool names the instrumented operation (read_file, edit_file, etc.).
// sideEffect reports whether the operation already mutated external state
// before this hook fired (used for post-hooks to record state visibility).
func (c *Controller) MaybeFire(hook Hook, seq uint64, tool string, sideEffect bool) Decision {
	c.mu.Lock()
	defer c.mu.Unlock()
	if armed, ok := c.armed[hook]; ok {
		delete(c.armed, hook)
		return c.recordLocked(Observation{Hook: hook, Fire: armed, Seq: seq, Tool: tool, SideEffect: sideEffect})
	}
	for index, planned := range c.schedule {
		if planned.Hook != hook {
			continue
		}
		if hook == HookEventStoreCommit && planned.AfterSeq != seq {
			continue
		}
		c.schedule = append(c.schedule[:index], c.schedule[index+1:]...)
		return c.recordLocked(Observation{Hook: hook, Fire: planned.FireKind, Seq: seq, Tool: tool, SideEffect: sideEffect})
	}
	return Decision{}
}

// Arm installs a one-shot fire that the next matching hook call consumes.
// Tests use Arm when they cannot express the trigger as an AfterSeq match
// (e.g., context-summary calls that are not EventStoreCommit events).
func (c *Controller) Arm(hook Hook, kind FireKind) error {
	if _, ok := knownHooks[hook]; !ok {
		return fmt.Errorf("chaos: arm unknown hook %q", hook)
	}
	if _, ok := knownFireKinds[kind]; !ok {
		return fmt.Errorf("chaos: arm unknown fire kind %q", kind)
	}
	c.mu.Lock()
	c.armed[hook] = kind
	c.mu.Unlock()
	return nil
}

// Observations returns a defensive copy of the fired observations in the
// order they were consumed.
func (c *Controller) Observations() []Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Observation, len(c.fired))
	copy(out, c.fired)
	return out
}

// HasFired reports whether at least one scheduled or armed fire was consumed.
func (c *Controller) HasFired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.fired) > 0
}

// PendingSchedules returns the unconsumed schedule entries. Tests use it to
// assert that every planned fire actually fired (or to detect unexpected
// leftovers after a run).
func (c *Controller) PendingSchedules() []PlannedFire {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]PlannedFire, len(c.schedule))
	copy(out, c.schedule)
	return out
}

// Summary returns a JSON-ready snapshot of the controller state.
type Summary struct {
	Seed      int64         `json:"seed"`
	Planned   []PlannedFire `json:"planned"`
	Fired     []Observation `json:"fired"`
	Remaining []PlannedFire `json:"remaining,omitempty"`
}

// Snapshot returns a defensive summary for evidence files.
func (c *Controller) Snapshot() Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Summary{
		Seed:      c.scheduleKey,
		Planned:   append([]PlannedFire(nil), c.original...),
		Fired:     append([]Observation(nil), c.fired...),
		Remaining: append([]PlannedFire(nil), c.schedule...),
	}
}

// StableSort sorts observations by Hook then Seq then Tool so evidence
// files are diffable across runs.
func StableSort(observations []Observation) {
	sort.SliceStable(observations, func(i, j int) bool {
		left, right := observations[i], observations[j]
		if left.Hook != right.Hook {
			return string(left.Hook) < string(right.Hook)
		}
		if left.Seq != right.Seq {
			return left.Seq < right.Seq
		}
		return left.Tool < right.Tool
	})
}

func (c *Controller) recordLocked(observation Observation) Decision {
	c.fired = append(c.fired, observation)
	switch observation.Fire {
	case FireAbort:
		return Decision{
			Fire:     FireAbort,
			Err:      fmt.Errorf("chaos: aborted at %s seq=%d tool=%s", observation.Hook, observation.Seq, observation.Tool),
			Observed: true,
		}
	case FireContext:
		return Decision{Fire: FireContext, Err: ErrChaosContextCanceled, Observed: true}
	case FireUnknown:
		return Decision{Fire: FireUnknown, Observed: true}
	default:
		return Decision{Fire: observation.Fire, Observed: true}
	}
}

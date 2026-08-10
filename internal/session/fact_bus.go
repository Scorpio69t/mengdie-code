// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"errors"
	"strings"
	"sync"
)

const DefaultPublicFactBuffer = 32

// PublicFactNotification carries one committed fact. Gap is set when this
// bounded subscription dropped an older notification; consumers must replay
// from their last applied SessionSeq before trusting the live item.
type PublicFactNotification struct {
	Fact PublicFact
	Gap  bool
}

// PublicFactSubscription is a closeable, receive-only committed-fact stream.
// Close is idempotent.
type PublicFactSubscription interface {
	Notifications() <-chan PublicFactNotification
	Close()
}

// CommittedFactPublisher is used by the store-first event adapter after a
// successful commit. Implementations must not block the Agent path.
type CommittedFactPublisher interface {
	PublishCommitted(PublicFact)
}

// PublicFactBus is a bounded same-process notification bus. It is disposable
// runtime state: EventStore replay remains the source of truth.
type PublicFactBus struct {
	mu          sync.Mutex
	capacity    int
	nextID      uint64
	subscribers map[uint64]*factSubscriber
}

type factSubscriber struct {
	sessionID string
	cursor    uint64
	channel   chan PublicFactNotification
}

type publicFactSubscription struct {
	bus     *PublicFactBus
	id      uint64
	channel <-chan PublicFactNotification
	once    sync.Once
}

func NewPublicFactBus(capacity int) *PublicFactBus {
	if capacity <= 0 {
		capacity = DefaultPublicFactBuffer
	}
	return &PublicFactBus{capacity: capacity, subscribers: make(map[uint64]*factSubscriber)}
}

func (bus *PublicFactBus) Subscribe(sessionID string, afterSeq uint64) (PublicFactSubscription, error) {
	if bus == nil {
		return nil, errors.New("public fact bus is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("public fact subscription session id is required")
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.nextID++
	channel := make(chan PublicFactNotification, bus.capacity)
	bus.subscribers[bus.nextID] = &factSubscriber{sessionID: sessionID, cursor: afterSeq, channel: channel}
	return &publicFactSubscription{bus: bus, id: bus.nextID, channel: channel}, nil
}

// PublishCommitted never waits for a consumer. When a buffer is full it
// replaces its oldest item and marks the new item as a replay-required gap.
func (bus *PublicFactBus) PublishCommitted(fact PublicFact) {
	if bus == nil || fact.validate() != nil {
		return
	}
	fact = clonePublicFact(fact)
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for _, subscriber := range bus.subscribers {
		if subscriber.sessionID != fact.SessionID || fact.SessionSeq <= subscriber.cursor {
			continue
		}
		notification := PublicFactNotification{Fact: clonePublicFact(fact)}
		select {
		case subscriber.channel <- notification:
		default:
			select {
			case <-subscriber.channel:
			default:
			}
			notification.Gap = true
			select {
			case subscriber.channel <- notification:
			default:
			}
		}
		subscriber.cursor = fact.SessionSeq
	}
}

func (subscription *publicFactSubscription) Notifications() <-chan PublicFactNotification {
	if subscription == nil {
		return nil
	}
	return subscription.channel
}

func (subscription *publicFactSubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() {
		subscription.bus.mu.Lock()
		defer subscription.bus.mu.Unlock()
		subscriber, ok := subscription.bus.subscribers[subscription.id]
		if !ok {
			return
		}
		delete(subscription.bus.subscribers, subscription.id)
		close(subscriber.channel)
	})
}

var _ CommittedFactPublisher = (*PublicFactBus)(nil)

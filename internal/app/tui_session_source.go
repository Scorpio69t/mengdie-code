// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tui"
)

// tuiSessionSource adapts the application-facing Session Service to the
// narrower consumer-owned TUI interface.
type tuiSessionSource struct{ service *session.Service }

func (source tuiSessionSource) ReplayPublicFacts(ctx context.Context, sessionID string, afterSeq uint64, limit int) (session.PublicFactPage, error) {
	return source.service.ReplayPublicFacts(ctx, sessionID, afterSeq, limit)
}

func (source tuiSessionSource) SubscribePublicFacts(sessionID string, afterSeq uint64) (tui.FactSubscription, error) {
	return source.service.SubscribePublicFacts(sessionID, afterSeq)
}

var _ tui.SessionFactSource = tuiSessionSource{}

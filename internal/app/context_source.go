// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"

	"github.com/Scorpio69t/mengdie-code/internal/session"
	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// sessionContextSourceReader binds the model-visible tool to one Session. The
// model never receives or chooses a session identity.
type sessionContextSourceReader struct {
	store     *session.SQLiteStore
	sessionID string
}

func (r sessionContextSourceReader) Describe(ctx context.Context) (tools.ContextSourceDescriptor, error) {
	summary, err := r.store.LoadLatestContextSummary(ctx, r.sessionID)
	if err != nil {
		return tools.ContextSourceDescriptor{}, err
	}
	return tools.ContextSourceDescriptor{
		SummarySHA256: summary.SHA256,
		SourceStart:   summary.SourceStart,
		SourceEnd:     summary.SourceEnd,
	}, nil
}

func (r sessionContextSourceReader) Load(
	ctx context.Context,
	expected tools.ContextSourceDescriptor,
) ([]tools.ContextSourceMessage, error) {
	_, messages, err := r.store.LoadContextSummarySource(ctx, r.sessionID, session.ContextSummaryIdentity{
		SHA256: expected.SummarySHA256, SourceStart: expected.SourceStart, SourceEnd: expected.SourceEnd,
	})
	if err != nil {
		return nil, err
	}
	result := make([]tools.ContextSourceMessage, len(messages))
	for index, message := range messages {
		result[index] = tools.ContextSourceMessage{
			Ordinal: message.Ordinal, Role: message.Message.Role,
			Completeness: string(message.Completeness), SHA256: message.SHA256,
			Message: message.Message,
		}
	}
	return result, nil
}

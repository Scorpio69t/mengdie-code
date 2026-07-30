// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

// Package terminal renders application events without coupling providers or
// tools to a concrete user interface.
package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/Scorpio69t/mengdie-code/internal/events"
)

// JSONRenderer writes exactly one complete JSON event per line.
type JSONRenderer struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func NewJSONRenderer(writer io.Writer) (*JSONRenderer, error) {
	if writer == nil {
		return nil, errors.New("JSON renderer writer is required")
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return &JSONRenderer{encoder: encoder}, nil
}

func (r *JSONRenderer) Emit(ctx context.Context, event events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.encoder.Encode(event)
}

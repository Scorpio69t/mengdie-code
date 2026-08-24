// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package chaos

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Scorpio69t/mengdie-code/internal/tools"
)

// WrapTool returns a chaos-aware wrapper for a single tool. The wrapper
// preserves Spec, Prepare, and the original Execute signature, and fires
// HookReadToolPre / HookReadToolPost around Execute.
func WrapTool(inner tools.Tool, ctrl *Controller) tools.Tool {
	if inner == nil {
		panic("chaos: wrap nil tool")
	}
	if ctrl == nil {
		panic("chaos: wrap nil controller")
	}
	spec := inner.Spec()
	return &wrappedTool{Tool: inner, name: spec.Name, ctrl: ctrl}
}

// WrapRegistry builds a new *tools.Registry where every entry from inner is
// replaced with a chaos-wrapped tool. Production code is untouched: callers
// pass the returned registry to agent.Options.Registry exactly like the
// original.
func WrapRegistry(inner *tools.Registry, ctrl *Controller) (*tools.Registry, error) {
	if inner == nil {
		return nil, errors.New("chaos: wrap nil registry")
	}
	if ctrl == nil {
		return nil, errors.New("chaos: wrap nil controller")
	}
	wrapped := make([]tools.Tool, 0, len(inner.Specs()))
	for _, spec := range inner.Specs() {
		tool, ok := inner.Lookup(spec.Name)
		if !ok {
			continue
		}
		wrapped = append(wrapped, WrapTool(tool, ctrl))
	}
	return tools.NewRegistry(wrapped...)
}

// wrappedTool delegates Spec/Prepare to the inner tool and fires the
// read-tool hooks around Execute. Write-tool hooks live on the journal
// wrapper because that is where the durable write boundary actually is.
type wrappedTool struct {
	tools.Tool
	name string
	ctrl *Controller
}

// Spec delegates so the agent runtime sees the original tool surface.
func (w *wrappedTool) Spec() tools.ToolSpec { return w.Tool.Spec() }

// Prepare delegates; chaos hooks live in Execute where side effects happen.
func (w *wrappedTool) Prepare(ctx context.Context, raw json.RawMessage, env tools.PrepareEnv) (*tools.PreparedCall, error) {
	return w.Tool.Prepare(ctx, raw, env)
}

// Execute fires HookReadToolPre before delegating and HookReadToolPost
// after the inner tool completes. The chaos journal wrapper handles write
// hooks (patch.pre / patch.post / patch.conflict) separately because they
// guard the durable MutationJournal boundary.
func (w *wrappedTool) Execute(ctx context.Context, call *tools.PreparedCall, cap tools.Capability, env tools.ExecEnv) (*tools.ToolResult, error) {
	pre := w.ctrl.MaybeFire(HookReadToolPre, 0, w.name, false)
	switch pre.Fire {
	case FireAbort:
		return nil, pre.Err
	case FireContext:
		return nil, pre.Err
	}
	result, err := w.Tool.Execute(ctx, call, cap, env)
	post := w.ctrl.MaybeFire(HookReadToolPost, 0, w.name, err == nil && result != nil)
	switch post.Fire {
	case FireAbort:
		return result, post.Err
	case FireContext:
		return result, post.Err
	}
	return result, err
}

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import "strings"

// defaultM1Tools returns the M1 base tool set in stable order. It is the
// canonical starting point for DefaultTools: feature-specific tools
// (e.g. memory_recall) are appended via options without disturbing the
// base ordering.
func defaultM1Tools() []Tool {
	return []Tool{
		NewReadFile(),
		NewListFiles(),
		NewSearchText(),
		NewEditFile(),
		NewWriteFile(),
		NewShell(),
		NewWriteTodos(),
	}
}

// defaultToolsConfig is the private accumulator consumed by
// DefaultToolsOption. Keeping it private means future fields (e.g.
// approval policy overrides) can be added without expanding the public
// surface.
type defaultToolsConfig struct {
	memoryRetriever MemoryRecallRetriever
	projectIdentity string
}

// DefaultToolsOption configures DefaultTools. Pass zero or more options
// to opt into feature-specific tools (e.g. memory_recall). The base M1
// tool set is always returned in stable order.
type DefaultToolsOption func(*defaultToolsConfig)

// WithMemoryRetriever appends the memory_recall tool to the default set
// when retriever is non-nil. A nil retriever is silently ignored so
// callers can pass nil defensively without breaking registration.
func WithMemoryRetriever(retriever MemoryRecallRetriever) DefaultToolsOption {
	return func(c *defaultToolsConfig) {
		c.memoryRetriever = retriever
	}
}

// WithProjectIdentityForTools sets the project-scope identity forwarded
// to tools that need a target scope value (e.g. memory_recall's
// catalogue injection). Whitespace is trimmed; an empty result leaves
// the default user scope.
//
// Note: this option is named WithProjectIdentityForTools (not
// WithProjectIdentity) because memory_recall.go already exports
// WithProjectIdentity as a MemoryRecallOption, and Go does not allow
// two same-named package-level functions regardless of return type.
// Callers that want to set both the default-tools identity and the
// memory_recall identity pass them as separate options; memory_recall
// receives the resolved identity through its own WithProjectIdentity
// inside DefaultTools.
func WithProjectIdentityForTools(projectIdentity string) DefaultToolsOption {
	return func(c *defaultToolsConfig) {
		c.projectIdentity = strings.TrimSpace(projectIdentity)
	}
}

// DefaultTools returns the M1 tool set plus feature-specific tools
// enabled by the provided options. Calling DefaultTools() with no
// arguments is byte-compatible with the pre-variadic implementation and
// does NOT append memory_recall.
func DefaultTools(opts ...DefaultToolsOption) []Tool {
	cfg := defaultToolsConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	out := defaultM1Tools()
	if cfg.memoryRetriever != nil {
		out = append(out, NewMemoryRecallTool(
			cfg.memoryRetriever,
			WithProjectIdentity(cfg.projectIdentity),
		))
	}
	return out
}

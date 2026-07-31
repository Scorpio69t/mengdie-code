// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Registry is the Agent loop's view of available tools. It is built once
// per process and is safe for concurrent reads.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry registers tools, rejecting duplicate names and invalid specs
// at construction time so the Agent loop never discovers them mid-run.
func NewRegistry(all ...Tool) (*Registry, error) {
	registry := &Registry{tools: make(map[string]Tool, len(all))}
	for _, tool := range all {
		spec := tool.Spec()
		if spec.Name == "" {
			return nil, fmt.Errorf("tools: registry: tool with empty name")
		}
		if len(spec.InputSchema) > 0 && !json.Valid(spec.InputSchema) {
			return nil, fmt.Errorf("tools: registry: %s has invalid input schema", spec.Name)
		}
		if len(spec.Effects) == 0 {
			return nil, fmt.Errorf("tools: registry: %s declares no effects", spec.Name)
		}
		if _, exists := registry.tools[spec.Name]; exists {
			return nil, fmt.Errorf("tools: registry: duplicate tool %q", spec.Name)
		}
		registry.tools[spec.Name] = tool
	}
	return registry, nil
}

// Lookup returns the tool registered under name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// Specs returns every registered spec sorted by name for stable prompts
// and provider tool lists.
func (r *Registry) Specs() []ToolSpec {
	specs := make([]ToolSpec, 0, len(r.tools))
	for _, tool := range r.tools {
		specs = append(specs, tool.Spec())
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}

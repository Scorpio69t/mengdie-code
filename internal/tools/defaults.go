// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

// DefaultTools returns the M1 tool set in stable order. Registration into
// a Registry happens at the application boundary.
func DefaultTools() []Tool {
	return []Tool{
		NewReadFile(),
		NewListFiles(),
		NewSearchText(),
	}
}

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewRunID returns an opaque, locally generated 128-bit run identifier.
func NewRunID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate run id: %w", err)
	}
	return "run_" + hex.EncodeToString(value[:]), nil
}

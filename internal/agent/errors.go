// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package agent

import "errors"

var (
	ErrMaxTurns        = errors.New("agent reached maximum turns")
	ErrRepeatedCall    = errors.New("agent repeated the same tool call three times")
	ErrRepeatedFailure = errors.New("agent repeated the same tool failure three times")
	ErrInvalidResponse = errors.New("provider returned an invalid response")
)

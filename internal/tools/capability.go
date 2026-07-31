// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import "errors"

// ErrCapabilityMismatch rejects a capability bound to a different tool or
// digest than the call being executed.
var ErrCapabilityMismatch = errors.New("capability does not match prepared call")

// CheckCapability enforces the minimal protocol invariant every Execute
// relies on: the capability exists and binds this exact tool name and
// argument digest. Expiry, nonce consumption and policy binding are minted
// and verified by Approval in P1-06.
func CheckCapability(call *PreparedCall, cap Capability) error {
	if cap.Nonce == "" {
		return ErrCapabilityMissing
	}
	if cap.ToolName != call.ToolName || cap.Digest != call.Digest {
		return ErrCapabilityMismatch
	}
	return nil
}

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonicalize normalizes raw tool arguments into a stable JSON form:
// object keys sorted, insignificant whitespace removed. Two calls with
// semantically identical arguments must produce identical canonical bytes,
// because Approval binds the digest of these bytes.
func Canonicalize(raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("tools: canonicalize: invalid JSON")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("tools: canonicalize: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("tools: canonicalize: %w", err)
	}
	return canonical, nil
}

// ComputeDigest binds a tool name and its canonical argument into one
// SHA-256 digest. The NUL separator prevents name/argument ambiguity.
func ComputeDigest(toolName string, canonicalArg json.RawMessage) string {
	sum := sha256.New()
	sum.Write([]byte(toolName))
	sum.Write([]byte{0})
	sum.Write(canonicalArg)
	return hex.EncodeToString(sum.Sum(nil))
}

// decodeArgs unmarshals raw tool arguments strictly: unknown fields are
// rejected so model typos surface as errors instead of silent no-ops.
func decodeArgs(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("tools: decode arguments: %w", err)
	}
	return nil
}

// PrepareCall is the finishing step every tool's Prepare should reuse: it
// validates the raw argument, canonicalizes it and computes the digest, so
// no tool can forget the binding that Approval relies on.
func PrepareCall(id, toolName string, raw json.RawMessage, effects []Effect, preview Preview, preconditions []Precondition) (*PreparedCall, error) {
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	call := &PreparedCall{
		ID:            id,
		ToolName:      toolName,
		CanonicalArg:  canonical,
		Effects:       effects,
		Preview:       preview,
		Preconditions: preconditions,
		Digest:        ComputeDigest(toolName, canonical),
	}
	if err := call.Validate(); err != nil {
		return nil, err
	}
	return call, nil
}

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type ErrorCategory string

const (
	ErrorInvalidRequest ErrorCategory = "invalid_request"
	ErrorAuthentication ErrorCategory = "authentication"
	ErrorPermission     ErrorCategory = "permission"
	ErrorRateLimit      ErrorCategory = "rate_limit"
	ErrorTimeout        ErrorCategory = "timeout"
	ErrorServer         ErrorCategory = "server"
	ErrorNetwork        ErrorCategory = "network"
	ErrorProtocol       ErrorCategory = "protocol"
	ErrorCanceled       ErrorCategory = "canceled"
	ErrorSink           ErrorCategory = "sink"
)

// Error carries stable machine-readable classification without retaining an
// HTTP response body, request body, credential, or complete prompt.
type Error struct {
	Category   ErrorCategory
	StatusCode int
	Code       string
	RequestID  string
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	parts := []string{"provider " + string(e.Category)}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", e.StatusCode))
	}
	if e.Code != "" {
		parts = append(parts, "code="+e.Code)
	}
	if e.RequestID != "" {
		parts = append(parts, "request_id="+e.RequestID)
	}
	message := strings.Join(parts, " ")
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func AsError(err error) (*Error, bool) {
	var providerErr *Error
	if errors.As(err, &providerErr) {
		return providerErr, true
	}
	return nil, false
}

func CategoryOf(err error) ErrorCategory {
	providerErr, ok := AsError(err)
	if !ok {
		return ""
	}
	return providerErr.Category
}

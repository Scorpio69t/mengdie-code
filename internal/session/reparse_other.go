// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package session

func isReparsePoint(string) (bool, error) { return false, nil }

// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package evaluation

func realPathIsReparse(string) (bool, error) { return false, nil }

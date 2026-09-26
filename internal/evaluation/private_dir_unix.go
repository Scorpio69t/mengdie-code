// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package evaluation

import "os"

func createPrivateRealRepositoryRoot() (string, error) {
	root, err := os.MkdirTemp("", "mengdie-real-repo-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return "", err
	}
	return root, nil
}

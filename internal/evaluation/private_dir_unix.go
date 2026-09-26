// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package evaluation

import "os"

func secureRealRepositoryRoot(path string) error {
	return os.Chmod(path, 0o700)
}

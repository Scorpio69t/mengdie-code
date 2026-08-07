// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package session

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func isReparsePoint(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, fmt.Errorf("encode MengDie data directory path: %w", err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return false, fmt.Errorf("inspect MengDie data directory attributes: %w", err)
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

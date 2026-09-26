// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package evaluation

import "golang.org/x/sys/windows"

func realPathIsReparse(path string) (bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

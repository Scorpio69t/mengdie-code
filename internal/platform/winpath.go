// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

package platform

import (
	"fmt"
	"strings"
)

// windowsReservedNames are DOS device names that must never be used as file
// names on Windows, matched by basename without extension, case-insensitively.
var windowsReservedNames = map[string]struct{}{
	"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
	"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {},
	"COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
	"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {},
	"LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// checkWindowsSyntax rejects path forms that bypass ordinary normalization
// before any cleaning happens: extended-length and device namespaces, UNC,
// and drive-relative paths.
func checkWindowsSyntax(path string) error {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return fmt.Errorf("path guard: %w: %q", ErrDevicePath, path)
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
		return fmt.Errorf("path guard: %w: %q", ErrUNCPath, path)
	}
	if len(path) >= 2 && isDriveLetter(path[0]) && path[1] == ':' &&
		(len(path) == 2 || (path[2] != '\\' && path[2] != '/')) {
		return fmt.Errorf("path guard: %w: %q", ErrDriveRelative, path)
	}
	if strings.HasPrefix(path, `\`) {
		// Rooted to the current drive, which is ambiguous for a guard
		// anchored at an explicit project root.
		return fmt.Errorf("path guard: %w: %q", ErrDriveRelative, path)
	}
	return nil
}

// checkWindowsComponents rejects ADS syntax and reserved device names in the
// cleaned absolute path. The drive letter is stripped manually (not via
// filepath.VolumeName) so the check behaves identically on every host.
func checkWindowsComponents(cleaned string) error {
	rest := cleaned
	if len(rest) >= 2 && isDriveLetter(rest[0]) && rest[1] == ':' {
		rest = rest[2:]
	}
	for _, component := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		if strings.ContainsRune(component, ':') {
			return fmt.Errorf("path guard: %w: %q", ErrADS, cleaned)
		}
		name := component
		if i := strings.IndexByte(name, '.'); i >= 0 {
			name = name[:i]
		}
		if _, reserved := windowsReservedNames[strings.ToUpper(name)]; reserved {
			return fmt.Errorf("path guard: %w: %q", ErrDevicePath, cleaned)
		}
	}
	return nil
}

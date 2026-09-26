// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package evaluation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateRealRepositoryRoot() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()

	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	sid := user.User.Sid
	var pinner runtime.Pinner
	pinner.Pin(sid)
	defer pinner.Unpin()

	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return "", err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return "", err
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return "", err
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return "", err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	tempDir := os.TempDir()
	for range 10 {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		root := filepath.Join(tempDir, "mengdie-real-repo-"+hex.EncodeToString(suffix[:]))
		rootPointer, err := windows.UTF16PtrFromString(root)
		if err != nil {
			return "", err
		}
		if err := windows.CreateDirectory(rootPointer, &attributes); err == nil {
			return root, nil
		} else if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) && !errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a unique private temporary directory")
}

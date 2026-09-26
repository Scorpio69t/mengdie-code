// Copyright 2026 MengDie Code Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package evaluation

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSecureRealRepositoryRootRestrictsDACLToCurrentUser(t *testing.T) {
	root, err := createPrivateRealRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}

	descriptor, err := windows.GetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor == nil {
		t.Fatal("directory has no security descriptor")
	}
	got := descriptor.String()
	wantACE := "(A;OICI;GA;;;" + user.User.Sid.String() + ")"
	if strings.HasSuffix(user.User.Sid.String(), "-500") {
		wantACE = "(A;OICI;GA;;;LA)"
	}
	if !strings.HasPrefix(got, "D:P") || strings.Count(got, "(") != 1 || !strings.Contains(got, wantACE) {
		t.Fatalf("directory DACL = %q, want protected current-user ACE %q", got, wantACE)
	}
}

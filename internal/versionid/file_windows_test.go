// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package versionid

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsKeyRejectsPublicAndUnsupportedACLs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "key")
	if _, err := Init(path); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, sddl := range []string{
		"O:WDD:P(A;;FA;;;WD)",
		"O:" + user.User.Sid.String() + "D:P(A;;FR;;;WD)",
		"O:" + user.User.Sid.String() + "D:P",
		"O:" + user.User.Sid.String() + "D:NO_ACCESS_CONTROL",
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkDescriptor(sd, false); err == nil {
			t.Fatalf("unprotected descriptor accepted: %s", sddl)
		}
	}
	// A preexisting leaf with inherited public read grants must be rejected
	// before a tempfile can expose even its initial empty handle.
	dir := filepath.Dir(path)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("public leaf accepted")
	}
	if _, err := Init(filepath.Join(dir, "another-key")); err == nil {
		t.Fatal("unsafe creation accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "another-key")); !os.IsNotExist(err) {
		t.Fatal("unsafe key published")
	}
}

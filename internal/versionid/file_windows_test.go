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

func TestWindowsServicingOwnerAndAncestorRights(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	installer, _, _, err := windows.LookupSID("", `NT SERVICE\TrustedInstaller`)
	if err != nil {
		t.Skip("OS servicing identity is not installed")
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + installer.String() + "D:P(A;;FA;;;" + installer.String() + ")(A;;FA;;;BA)(A;;FA;;;SY)(A;;GR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDescriptor(sd, true); err != nil {
		t.Fatal("trusted servicing ancestor refused", err)
	}
	if err := checkDescriptor(sd, false); err == nil {
		t.Fatal("servicing owner/read grant accepted as private leaf")
	}
	for _, rights := range []string{"0x00000006", "0x00000116", "0x00000040", "0x00040000"} {
		sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;" + rights + ";;;WD)")
		if err != nil {
			t.Fatal(err)
		}
		got := checkDescriptor(sd, true)
		if (got == nil) != (rights == "0x00000006") {
			t.Fatalf("ancestor rights policy mismatch: %s %v", rights, got)
		}
	}
}

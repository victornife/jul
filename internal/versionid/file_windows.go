// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package versionid

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"
)

func trustedSID(sid *windows.SID, user *windows.SID) bool {
	system, _ := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	admins, _ := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	return sid.Equals(user) || sid.Equals(system) || sid.Equals(admins)
}
func checkDescriptor(sd *windows.SECURITY_DESCRIPTOR, directory bool) error {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trustedSID(owner, tokenUser.User.Sid) {
		return errors.New("untrusted key owner")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return errors.New("missing private key DACL")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported key ACL grant")
		}
		if directory && ace.Mask&(windows.GENERIC_ALL|windows.GENERIC_WRITE|windows.WRITE_DAC|windows.WRITE_OWNER|windows.DELETE|windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|0x0040 /* FILE_DELETE_CHILD */) == 0 {
			continue
		}
		if !trustedSID((*windows.SID)(unsafe.Pointer(&ace.SidStart)), tokenUser.User.Sid) {
			return errors.New("untrusted key ACL grant")
		}
	}
	return nil
}
func checkDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	leaf := absolute
	for {
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() {
			return errors.New("unsafe key directory")
		}
		sd, err := windows.GetNamedSecurityInfo(absolute, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		// The leaf must have exclusively trusted grants (including inherited
		// read grants): a permissive CreateTemp followed by ACL repair cannot
		// revoke a readable handle opened before repair. Ancestors may be read-only.
		if err := checkDescriptor(sd, absolute != leaf); err != nil {
			return err
		}
		parent := filepath.Dir(absolute)
		if parent == absolute {
			return nil
		}
		absolute = parent
	}
}

func checkFile(f *os.File, _ os.FileInfo) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return checkDescriptor(sd, false)
}
func protectFile(f *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// os.CreateTemp opens without WRITE_DAC. Named security opens the required
	// right; the exclusively trusted leaf was verified before file creation.
	return windows.SetNamedSecurityInfo(f.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// Create directories with their protected DACL atomically. A competing
// initializer never observes a permissive directory-then-repair window.
func makePrivateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() {
			return errors.New("unsafe key directory")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return errors.New("missing key volume")
	}
	if err := makePrivateDirectory(parent); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	err = windows.CreateDirectory(name, &sa)
	runtime.KeepAlive(sd)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return err
	}
	return nil
}

//go:build windows

package api

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protectFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current Windows user: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build Windows user-only ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("set Windows user-only ACL: %w", err)
	}
	return verifyProtectedFile(path)
}

func verifyProtectedFile(path string) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read Windows endpoint ACL: %w", err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return fmt.Errorf("read Windows endpoint ACL control: %w", err)
	}
	if control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("windows endpoint DACL is absent or inherited")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read Windows endpoint DACL: %w", err)
	}
	if dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("windows endpoint DACL has %d entries, want 1", daclEntryCount(dacl))
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return fmt.Errorf("read Windows endpoint access entry: %w", err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errors.New("windows endpoint ACL entry is not an allow entry")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current Windows user: %w", err)
	}
	entrySID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !entrySID.Equals(user.User.Sid) {
		return errors.New("windows endpoint ACL does not belong to the current user")
	}
	return nil
}

func daclEntryCount(dacl *windows.ACL) uint16 {
	if dacl == nil {
		return 0
	}
	return dacl.AceCount
}

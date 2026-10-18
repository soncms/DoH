//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"

	"golang.org/x/sys/windows"
)

// prepareGUIHandoffAccess lets a separately authenticated administrator wait
// for this process to exit. It grants only SYNCHRONIZE, never process control
// or debug privileges. Keep the grant until exit after a successful launch;
// restore it if the credential prompt is cancelled or launching fails.
func prepareGUIHandoffAccess() (restore func() error, err error) {
	process := windows.CurrentProcess()
	original, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read GUI process permissions: %w", err)
	}
	before, _, err := original.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && before == nil) {
		return func() error { return nil }, nil // An absent/null DACL already permits waiting.
	}
	if err != nil {
		return nil, fmt.Errorf("read GUI process DACL: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	pin.Pin(admins)
	defer pin.Unpin()
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.ACCESS_MASK(windows.SYNCHRONIZE),
		AccessMode:        windows.GRANT_ACCESS, Inheritance: windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID,
			TrusteeType: windows.TRUSTEE_IS_GROUP, TrusteeValue: windows.TrusteeValueFromSID(admins)},
	}}, before)
	if err != nil {
		return nil, fmt.Errorf("prepare GUI wait permission: %w", err)
	}
	set := func(dacl *windows.ACL) error {
		return windows.SetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	}
	if err := set(acl); err != nil {
		return nil, fmt.Errorf("grant administrator GUI wait permission: %w", err)
	}
	granted, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read granted GUI process permissions: %w", err), set(before))
	}
	return func() error {
		current, err := windows.GetSecurityInfo(process, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		if current.String() == original.String() {
			return nil
		}
		if current.String() != granted.String() {
			return errors.New("GUI process permissions changed concurrently; original DACL was not restored")
		}
		if err := set(before); err != nil {
			return fmt.Errorf("restore GUI process permissions: %w", err)
		}
		return nil
	}, nil
}

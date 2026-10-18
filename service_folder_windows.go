//go:build windows

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type serviceACLMode uint8

const (
	serviceACLFile serviceACLMode = iota
	serviceACLDirectory
	serviceACLAncestor
	serviceACLVolume
)

const portableServiceSDDL = "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;BU)"

// Only identities whose effective authority cannot be obtained by a filtered
// desktop token may own or write service code/config. A user's own SID is not
// sufficient, even when that user is a member of Administrators.
func trustedServiceSID(sid *windows.SID) bool {
	if sid == nil {
		return false
	}
	switch sid.String() {
	case "S-1-5-18", "S-1-5-32-544", "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464":
		return true // SYSTEM, Administrators, TrustedInstaller
	}
	return false
}

func validateServiceACL(sd *windows.SECURITY_DESCRIPTOR, mode serviceACLMode) error {
	owner, _, err := sd.Owner()
	if err != nil || !trustedServiceSID(owner) {
		return errors.New("owner must be Administrators or SYSTEM")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("a restrictive file permission list is required")
	}
	// FILE_WRITE_DATA/ADD_FILE, APPEND_DATA/ADD_SUBDIRECTORY, WRITE_EA,
	// DELETE_CHILD and WRITE_ATTRIBUTES. All permit changing an app tree.
	dangerous := uint32(windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | windows.GENERIC_WRITE | 0x2 | 0x4 | 0x10 | 0x40 | 0x100)
	if mode == serviceACLVolume {
		// A volume root cannot be renamed/replaced. Windows normally lets users
		// create folders there, which is safe for a separately protected child.
		dangerous = windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_ALL | 0x40
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 && mode != serviceACLDirectory || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		// Do not guess at conditional/object-specific ACE layouts. Reject an
		// unfamiliar DACL instead of interpreting its bytes as an ordinary SID.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("unsupported file permission rule")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if uint32(ace.Mask)&dangerous != 0 && !trustedServiceSID(sid) {
			return fmt.Errorf("non-administrator %s can change service files or their path", sid.String())
		}
	}
	runtime.KeepAlive(sd)
	return nil
}

func validateServiceObject(path string, mode serviceACLMode) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("junctions and symlinks are not allowed in a service path")
	}
	if mode == serviceACLFile && info.NumberOfLinks != 1 {
		return errors.New("hard-linked service files are not allowed")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return validateServiceACL(sd, mode)
}

func validateServiceParent(root string) error {
	root = filepath.Clean(root)
	volume := filepath.VolumeName(root)
	if !filepath.IsAbs(root) || len(volume) != 2 || volume[1] != ':' {
		return errors.New("service folder must be on a local fixed disk")
	}
	volumeRoot := volume + string(os.PathSeparator)
	if strings.EqualFold(root, volumeRoot) {
		return errors.New("use a dedicated SonDNS folder, not a disk root")
	}
	p, _ := windows.UTF16PtrFromString(volumeRoot)
	if windows.GetDriveType(p) != windows.DRIVE_FIXED {
		return errors.New("service folder must be on a local fixed disk")
	}
	device, _ := windows.UTF16PtrFromString(volume)
	var mapping [1024]uint16
	if _, err := windows.QueryDosDevice(device, &mapping[0], uint32(len(mapping))); err != nil {
		return err
	}
	if strings.HasPrefix(windows.UTF16ToString(mapping[:]), `\??\`) {
		return errors.New("SUBST drive aliases cannot host the Windows service")
	}
	var flags uint32
	if err := windows.GetVolumeInformation(p, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return err
	}
	if flags&0x8 == 0 { // FILE_PERSISTENT_ACLS
		return errors.New("service folder requires a disk with Windows file permissions (NTFS/ReFS)")
	}
	// Verify from the volume downward before trusting a deeper path component.
	var parents []string
	for path := filepath.Dir(root); ; path = filepath.Dir(path) {
		parents = append(parents, path)
		if strings.EqualFold(path, volumeRoot) {
			break
		}
	}
	for i := len(parents) - 1; i >= 0; i-- {
		mode := serviceACLAncestor
		if i == len(parents)-1 {
			mode = serviceACLVolume
		}
		if err := validateServiceObject(parents[i], mode); err != nil {
			return fmt.Errorf("unsafe parent folder %s: %w", parents[i], err)
		}
	}
	return nil
}

func validateServiceTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mode := serviceACLFile
		if entry.IsDir() {
			mode = serviceACLDirectory
		}
		if err := validateServiceObject(path, mode); err != nil {
			return fmt.Errorf("unsafe service file %s: %w", path, err)
		}
		return nil
	})
}

func validatePortableServiceFolder(root string) error {
	if err := validateServiceParent(root); err != nil {
		return err
	}
	return validateServiceTree(root)
}

// Create a new directory with its restrictive ACL at creation time. Never
// recursively take ownership or rewrite permissions of a user/source folder.
func createServiceDirectory(path string) error {
	sd, err := windows.SecurityDescriptorFromString(portableServiceSDDL)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(p, &sa)
	runtime.KeepAlive(sd)
	return err
}

func createSecurePortableCopy(sourceExe, destination string, c Config) (string, error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return "", errors.New("Run SonDNS as administrator to create a service folder")
	}
	destination = filepath.Clean(destination)
	if err := validateServiceParent(destination); err != nil {
		return "", err
	}
	image, err := os.ReadFile(sourceExe)
	if err != nil {
		return "", err
	}
	c = c.Clone()
	if err := c.normalize(); err != nil {
		return "", err
	}
	if err := createServiceDirectory(destination); err != nil {
		return "", fmt.Errorf("create a new, unused portable folder %s: %w", destination, err)
	}
	// Do not recursively delete on failure. The directory was created securely;
	// preserve it for inspection/recovery and never remove an existing target.
	target := filepath.Join(destination, "SonDNS.exe")
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", err
	}
	_, err = file.Write(image)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", errors.Join(err, closeErr)
	}
	if err := SaveConfig(filepath.Join(destination, "data", "sondns.json"), c); err != nil {
		return "", err
	}
	if err := ensurePortableNoticesAt(destination); err != nil {
		return "", err
	}
	if err := validatePortableServiceFolder(destination); err != nil {
		return "", err
	}
	return target, nil
}

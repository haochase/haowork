//go:build windows

package corebridge

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func restrictInstancePermissions(path string, directory bool) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.WRITE_DAC|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return restrictOpenInstancePermissions(file, directory)
}

func restrictOpenInstancePermissions(file *os.File, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	allowed := []*windows.SID{user.User.Sid, system, administrators}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	var sddl strings.Builder
	sddl.WriteString("D:P")
	for _, sid := range allowed {
		sddl.WriteString("(A;")
		sddl.WriteString(inheritance)
		sddl.WriteString(";FA;;;")
		sddl.WriteString(sid.String())
		sddl.WriteString(")")
	}
	descriptor, err := windows.SecurityDescriptorFromString(sddl.String())
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return err
	}
	runtime.KeepAlive(descriptor)
	return verifyInstancePermissions(file, allowed)
}

func verifyInstancePermissions(file *os.File, allowed []*windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Core Bridge instance lock still inherits permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || int(dacl.AceCount) != len(allowed) {
		return errors.New("Core Bridge instance lock has unexpected access entries")
	}
	seen := make([]bool, len(allowed))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return err
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		match := -1
		for allowedIndex, sid := range allowed {
			if sid.Equals(aceSID) {
				match = allowedIndex
				break
			}
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || match < 0 || seen[match] {
			return errors.New("Core Bridge instance lock grants an unexpected principal")
		}
		seen[match] = true
	}
	return nil
}

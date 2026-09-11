//go:build windows

package executionreceipt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func openAnchoredFiles(projectRoot string) (*anchoredFiles, error) {
	project, err := openWindowsObject(0, windowsNTObjectPath(projectRoot), projectRoot, true, windows.FILE_OPEN)
	if err != nil {
		return nil, err
	}
	trustRootPath := filepath.Join(projectRoot, ".haowork")
	trustRoot, err := openWindowsObject(windows.Handle(project.Fd()), ".haowork", trustRootPath, true, windows.FILE_OPEN_IF)
	if err != nil {
		project.Close()
		return nil, err
	}
	files := &anchoredFiles{projectRoot: project, trustRoot: trustRoot}
	trustAllowed, err := windowsTrustRootSIDs()
	if err != nil {
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	if err := setRestrictedWindowsHandlePermissions(trustRoot, true, trustAllowed); err != nil {
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	directoryPath := filepath.Join(trustRootPath, "execution-receipts")
	files.directory, err = openWindowsObject(windows.Handle(trustRoot.Fd()), "execution-receipts", directoryPath, true, windows.FILE_OPEN_IF)
	if err != nil {
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	userAllowed, err := windowsCurrentUserSIDs()
	if err != nil {
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	if err := setRestrictedWindowsHandlePermissions(files.directory, true, userAllowed); err != nil {
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	files.events, err = openWindowsObject(windows.Handle(files.directory.Fd()), "events.jsonl", filepath.Join(directoryPath, "events.jsonl"), false, windows.FILE_OPEN_IF)
	if err != nil {
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	if err := setRestrictedWindowsHandlePermissions(files.events, false, userAllowed); err != nil {
		files.events.Close()
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	files.lock, err = openWindowsObject(windows.Handle(files.directory.Fd()), "events.lock", filepath.Join(directoryPath, "events.lock"), false, windows.FILE_OPEN_IF)
	if err != nil {
		files.events.Close()
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	if err := setRestrictedWindowsHandlePermissions(files.lock, false, userAllowed); err != nil {
		files.lock.Close()
		files.events.Close()
		files.directory.Close()
		trustRoot.Close()
		project.Close()
		return nil, err
	}
	return files, nil
}

func openWindowsObject(root windows.Handle, name, displayPath string, directory bool, disposition uint32) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	options := uint32(windows.FILE_SYNCHRONOUS_IO_NONALERT | windows.FILE_OPEN_REPARSE_POINT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.WRITE_DAC,
		attributes,
		&status,
		nil,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		options,
		0,
		0,
	)
	if err != nil {
		return nil, mapWindowsAnchoredError(err)
	}
	file := os.NewFile(uintptr(handle), displayPath)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, ErrStoreUnavailable
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() != directory {
		file.Close()
		return nil, fmt.Errorf("%w: unexpected object type for %q", ErrUnsafeStorePath, displayPath)
	}
	var tag fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&tag)), uint32(unsafe.Sizeof(tag))); err != nil {
		file.Close()
		return nil, err
	}
	if tag.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		file.Close()
		return nil, fmt.Errorf("%w: reparse point %q", ErrUnsafeStorePath, displayPath)
	}
	return file, nil
}

func windowsCurrentUserSIDs() ([]*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return []*windows.SID{user.User.Sid}, nil
}

func windowsTrustRootSIDs() ([]*windows.SID, error) {
	current, err := windowsCurrentUserSIDs()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	return append(current, system, administrators), nil
}

func setRestrictedWindowsHandlePermissions(file *os.File, directory bool, allowed []*windows.SID) error {
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
	err = windows.SetSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return err
	}
	return verifyRestrictedWindowsHandlePermissions(file, allowed)
}

func verifyRestrictedWindowsHandlePermissions(file *os.File, allowed []*windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("anchored object still inherits permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || int(dacl.AceCount) != len(allowed) {
		return errors.New("anchored object has unexpected access entries")
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
			return errors.New("anchored object grants an unexpected principal")
		}
		seen[match] = true
	}
	return nil
}

func mapWindowsAnchoredError(err error) error {
	if errors.Is(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED) || errors.Is(err, windows.STATUS_NOT_A_DIRECTORY) || errors.Is(err, windows.STATUS_FILE_IS_A_DIRECTORY) || errors.Is(err, windows.STATUS_OBJECT_TYPE_MISMATCH) || errors.Is(err, windows.ERROR_DIRECTORY) {
		return fmt.Errorf("%w: %v", ErrUnsafeStorePath, err)
	}
	return err
}

func lockAnchoredFile(ctx context.Context, file *os.File, shared bool) (func() error, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if !shared {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	overlapped := &windows.Overlapped{}
	for {
		err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, overlapped)
		if err == nil {
			return func() error { return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped) }, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, err
		}
		timer := time.NewTimer(lockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func windowsNTObjectPath(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\?\UNC\`)
	case strings.HasPrefix(path, `\\?\`):
		return `\??\` + strings.TrimPrefix(path, `\\?\`)
	case strings.HasPrefix(path, `\\`):
		return `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	default:
		return `\??\` + path
	}
}

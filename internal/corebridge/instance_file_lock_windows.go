//go:build windows

package corebridge

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type windowsInstanceLock struct {
	root, governance, directory, file *os.File
	overlapped                        *windows.Overlapped
}

func acquirePlatformInstanceLock(rootPath, governancePath, directoryPath, lockPath string) (platformInstanceLock, bool, error) {
	lock := &windowsInstanceLock{overlapped: &windows.Overlapped{}}
	var err error
	cleanup := func(err error) (platformInstanceLock, bool, error) {
		_ = lock.Close()
		return nil, false, err
	}
	lock.root, err = openWindowsInstancePath(rootPath, true, false)
	if err != nil {
		return nil, false, err
	}
	lock.governance, err = openWindowsInstancePath(governancePath, true, false)
	if err != nil {
		return cleanup(err)
	}
	lock.directory, err = openWindowsInstancePath(directoryPath, true, false)
	if err != nil {
		return cleanup(err)
	}
	lock.file, err = openWindowsInstancePath(lockPath, false, true)
	if err != nil {
		return cleanup(err)
	}
	err = windows.LockFileEx(windows.Handle(lock.file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lock.overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		_ = lock.Close()
		return nil, true, nil
	}
	if err != nil {
		return cleanup(err)
	}
	return lock, false, nil
}

func openWindowsInstancePath(path string, directory, create bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	desired := uint32(windows.GENERIC_READ | windows.READ_CONTROL)
	disposition := uint32(windows.OPEN_EXISTING)
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	} else {
		desired |= windows.GENERIC_WRITE | windows.WRITE_DAC
		if create {
			disposition = windows.OPEN_ALWAYS
		}
	}
	handle, err := windows.CreateFile(name, desired, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, disposition, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func (lock *windowsInstanceLock) File() *os.File { return lock.file }

func (lock *windowsInstanceLock) Close() error {
	var failures []error
	if lock.file != nil {
		failures = append(failures, windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, lock.overlapped), lock.file.Close())
	}
	if lock.directory != nil {
		failures = append(failures, lock.directory.Close())
	}
	if lock.governance != nil {
		failures = append(failures, lock.governance.Close())
	}
	if lock.root != nil {
		failures = append(failures, lock.root.Close())
	}
	return errors.Join(failures...)
}

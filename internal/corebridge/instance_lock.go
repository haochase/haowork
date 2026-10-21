package corebridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrInstanceAlreadyRunning = errors.New("Core Bridge instance already holds the shared state lock")
	ErrUnsafeInstanceLockPath = errors.New("Core Bridge instance lock path is unsafe")
)

type InstanceLock struct {
	lock platformInstanceLock
}

type platformInstanceLock interface {
	Close() error
	File() *os.File
}

// AcquireInstanceLock holds a non-blocking, process-lifetime lock inside the
// shared Core state root. Production must keep the returned handle open until
// the HTTP server shuts down.
func AcquireInstanceLock(projectRoot string) (*InstanceLock, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil || root == "" {
		return nil, ErrUnsafeInstanceLockPath
	}
	rootInfo, err := safeDirectoryInfo(root, false)
	if err != nil {
		return nil, err
	}
	governancePath := filepath.Join(root, ".haowork")
	governanceInfo, err := secureDirectory(governancePath)
	if err != nil {
		return nil, err
	}
	directoryPath := filepath.Join(governancePath, "core-bridge")
	directoryInfo, err := secureDirectory(directoryPath)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(directoryPath, "instance.lock")
	if info, statErr := os.Lstat(lockPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: lock is not a regular file", ErrUnsafeInstanceLockPath)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	fileLock, busy, err := acquirePlatformInstanceLock(root, governancePath, directoryPath, lockPath)
	if err != nil {
		return nil, err
	}
	if busy {
		return nil, ErrInstanceAlreadyRunning
	}
	if err := validateLockedPath(root, rootInfo, governancePath, governanceInfo, directoryPath, directoryInfo, lockPath, fileLock.File()); err != nil {
		_ = fileLock.Close()
		return nil, err
	}
	return &InstanceLock{lock: fileLock}, nil
}

func (lock *InstanceLock) Close() error {
	if lock == nil || lock.lock == nil {
		return nil
	}
	return lock.lock.Close()
}

func secureDirectory(path string) (os.FileInfo, error) {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := safeDirectoryInfo(path, true)
	if err != nil {
		return nil, err
	}
	if err := restrictInstancePermissions(path, true); err != nil {
		return nil, err
	}
	return info, nil
}

func safeDirectoryInfo(path string, managed bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: directory %q is not a direct directory", ErrUnsafeInstanceLockPath, path)
	}
	if managed && info.Mode().Perm()&0o077 != 0 {
		if err := restrictInstancePermissions(path, true); err != nil {
			return nil, err
		}
	}
	return info, nil
}

func validateLockedPath(root string, rootInfo os.FileInfo, governancePath string, governanceInfo os.FileInfo, directoryPath string, directoryInfo os.FileInfo, lockPath string, lockedFile *os.File) error {
	checks := []struct {
		path string
		want os.FileInfo
	}{{root, rootInfo}, {governancePath, governanceInfo}, {directoryPath, directoryInfo}}
	for _, check := range checks {
		current, err := safeDirectoryInfo(check.path, check.path != root)
		if err != nil || !os.SameFile(check.want, current) {
			return fmt.Errorf("%w: directory identity changed", ErrUnsafeInstanceLockPath)
		}
	}
	lockInfo, err := os.Lstat(lockPath)
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: lock is not a regular file", ErrUnsafeInstanceLockPath)
	}
	handleInfo, err := lockedFile.Stat()
	if err != nil || !os.SameFile(handleInfo, lockInfo) {
		return fmt.Errorf("%w: locked file does not match instance.lock", ErrUnsafeInstanceLockPath)
	}
	if err := restrictOpenInstancePermissions(lockedFile, false); err != nil {
		return err
	}
	return nil
}

//go:build !windows

package corebridge

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type unixInstanceLock struct {
	root, governance, directory, file *os.File
}

func acquirePlatformInstanceLock(rootPath, governancePath, directoryPath, lockPath string) (platformInstanceLock, bool, error) {
	root, err := openUnixInstancePath(rootPath, true, false)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(root.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		root.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, true, nil
		}
		return nil, false, err
	}
	lock := &unixInstanceLock{root: root}
	cleanup := func(err error) (platformInstanceLock, bool, error) {
		_ = lock.Close()
		return nil, false, err
	}
	lock.governance, err = openUnixInstancePath(governancePath, true, false)
	if err != nil {
		return cleanup(err)
	}
	lock.directory, err = openUnixInstancePath(directoryPath, true, false)
	if err != nil {
		return cleanup(err)
	}
	lock.file, err = openUnixInstancePath(lockPath, false, true)
	if err != nil {
		return cleanup(err)
	}
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			_ = lock.Close()
			return nil, true, nil
		}
		return cleanup(err)
	}
	return lock, false, nil
}

func openUnixInstancePath(path string, directory, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	} else {
		flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	}
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func (lock *unixInstanceLock) File() *os.File { return lock.file }

func (lock *unixInstanceLock) Close() error {
	var failures []error
	if lock.file != nil {
		failures = append(failures, unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close())
	}
	if lock.directory != nil {
		failures = append(failures, lock.directory.Close())
	}
	if lock.governance != nil {
		failures = append(failures, lock.governance.Close())
	}
	if lock.root != nil {
		failures = append(failures, unix.Flock(int(lock.root.Fd()), unix.LOCK_UN), lock.root.Close())
	}
	return errors.Join(failures...)
}

//go:build !windows

package executionreceipt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func openAnchoredFiles(projectRoot string) (*anchoredFiles, error) {
	projectFile, err := openUnixDirectoryChain(projectRoot)
	if err != nil {
		return nil, err
	}
	projectFD := int(projectFile.Fd())
	trustRootPath := filepath.Join(projectRoot, ".haowork")
	trustRoot, err := openUnixDirectoryAt(projectFD, ".haowork", trustRootPath)
	if err != nil {
		projectFile.Close()
		return nil, err
	}
	files := &anchoredFiles{projectRoot: projectFile, trustRoot: trustRoot}
	files.directory, err = openUnixDirectoryAt(int(files.trustRoot.Fd()), "execution-receipts", filepath.Join(trustRootPath, "execution-receipts"))
	if err != nil {
		files.trustRoot.Close()
		files.projectRoot.Close()
		return nil, err
	}
	files.events, err = openUnixFileAt(int(files.directory.Fd()), "events.jsonl", filepath.Join(trustRootPath, "execution-receipts", "events.jsonl"))
	if err != nil {
		files.directory.Close()
		files.trustRoot.Close()
		files.projectRoot.Close()
		return nil, err
	}
	files.lock, err = openUnixFileAt(int(files.directory.Fd()), "events.lock", filepath.Join(trustRootPath, "execution-receipts", "events.lock"))
	if err != nil {
		files.events.Close()
		files.directory.Close()
		files.trustRoot.Close()
		files.projectRoot.Close()
		return nil, err
	}
	return files, nil
}

func openUnixDirectoryChain(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(absolute)
	rootPath := string(filepath.Separator)
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapUnixAnchoredError(err)
	}
	current := os.NewFile(uintptr(rootFD), rootPath)
	if current == nil {
		unix.Close(rootFD)
		return nil, ErrStoreUnavailable
	}
	if clean == rootPath {
		return current, nil
	}
	components := strings.Split(strings.TrimPrefix(clean, rootPath), rootPath)
	currentPath := rootPath
	for _, component := range components {
		if component == "" {
			continue
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			current.Close()
			return nil, mapUnixAnchoredError(openErr)
		}
		currentPath = filepath.Join(currentPath, component)
		next := os.NewFile(uintptr(nextFD), currentPath)
		if next == nil {
			unix.Close(nextFD)
			current.Close()
			return nil, ErrStoreUnavailable
		}
		if err := current.Close(); err != nil {
			next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func openUnixDirectoryAt(parentFD int, name, displayPath string) (*os.File, error) {
	for {
		fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(parentFD, name, 0o700); errors.Is(mkdirErr, unix.EEXIST) {
				continue
			} else if mkdirErr != nil {
				return nil, mapUnixAnchoredError(mkdirErr)
			}
			continue
		}
		if err != nil {
			return nil, mapUnixAnchoredError(err)
		}
		if err := unix.Fchmod(fd, 0o700); err != nil {
			unix.Close(fd)
			return nil, err
		}
		file := os.NewFile(uintptr(fd), displayPath)
		if file == nil {
			unix.Close(fd)
			return nil, ErrStoreUnavailable
		}
		return file, nil
	}
}

func openUnixFileAt(parentFD int, name, displayPath string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, mapUnixAnchoredError(err)
	}
	file := os.NewFile(uintptr(fd), displayPath)
	if file == nil {
		unix.Close(fd)
		return nil, ErrStoreUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: leaf is not a regular file", ErrUnsafeStorePath)
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func mapUnixAnchoredError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EISDIR) {
		return fmt.Errorf("%w: %v", ErrUnsafeStorePath, err)
	}
	return err
}

func lockAnchoredFile(ctx context.Context, file *os.File, shared bool) (func() error, error) {
	operation := unix.LOCK_EX
	if shared {
		operation = unix.LOCK_SH
	}
	for {
		err := unix.Flock(int(file.Fd()), operation|unix.LOCK_NB)
		if err == nil {
			return func() error { return unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
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

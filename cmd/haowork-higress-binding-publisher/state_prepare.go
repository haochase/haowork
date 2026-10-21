package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	prepareStateDirCommand = "prepare-state-dir"
	managedStatePath       = "/var/lib/haowork/publisher-state/managed-consumers.json"
	publisherUID           = 65532
	publisherGID           = 65532
)

var errInvalidStatePreparation = errors.New("invalid managed consumer state preparation")

func parseCommand(args []string) (string, string, error) {
	if len(args) == 0 {
		return "run", "", nil
	}
	if len(args) != 2 || args[0] != prepareStateDirCommand || !isFixedManagedStatePath(args[1]) {
		return "", "", errInvalidStatePreparation
	}
	return prepareStateDirCommand, args[1], nil
}

func isFixedManagedStatePath(value string) bool {
	if strings.ContainsRune(value, '\x00') || !path.IsAbs(value) || path.Base(value) != "managed-consumers.json" || path.Clean(value) != value {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return value == managedStatePath
}

func prepareStateDirectory(statePath string, uid, gid int) error {
	if strings.ContainsRune(statePath, '\x00') || !filepath.IsAbs(statePath) || filepath.Base(statePath) != "managed-consumers.json" {
		return errInvalidStatePreparation
	}
	for _, part := range strings.FieldsFunc(statePath, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return errInvalidStatePreparation
		}
	}
	statePath = filepath.Clean(statePath)
	directory := filepath.Dir(statePath)
	if err := verifyPathComponents(directory, true); err != nil {
		return errInvalidStatePreparation
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("create managed consumer state directory")
	}
	if err := verifyPathComponents(directory, false); err != nil {
		return errInvalidStatePreparation
	}
	if err := os.Chown(directory, 0, 0); err != nil {
		return errors.New("take ownership of managed consumer state directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return errors.New("protect managed consumer state directory")
	}
	if info, err := os.Lstat(statePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errInvalidStatePreparation
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errInvalidStatePreparation
	}
	if err := os.Chown(directory, uid, gid); err != nil {
		return errors.New("assign managed consumer state directory")
	}
	if err := verifyPreparedStateDirectory(directory, uid, gid); err != nil {
		return errInvalidStatePreparation
	}
	return nil
}

func verifyPathComponents(target string, allowMissing bool) error {
	target = filepath.Clean(target)
	for current := target; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errInvalidStatePreparation
			}
			if current != target && !info.IsDir() {
				return errInvalidStatePreparation
			}
		} else if !allowMissing || !errors.Is(err, os.ErrNotExist) {
			return errInvalidStatePreparation
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func verifyPreparedStateDirectory(directory string, uid, gid int) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errInvalidStatePreparation
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || !sameFilesystemPath(directory, resolved) {
		return errInvalidStatePreparation
	}
	return verifyStateDirectoryOwner(info, uid, gid)
}

func sameFilesystemPath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

package corebridge_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/haochase/haowork/internal/corebridge"
)

func TestInstanceLockRejectsSecondProcessAndReleasesCleanly(t *testing.T) {
	root := t.TempDir()
	first, err := corebridge.AcquireInstanceLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corebridge.AcquireInstanceLock(root); !errors.Is(err, corebridge.ErrInstanceAlreadyRunning) {
		t.Fatalf("second AcquireInstanceLock() error=%v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := corebridge.AcquireInstanceLock(root)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{filepath.Join(root, ".haowork"), filepath.Join(root, ".haowork", "core-bridge")} {
			info, statErr := os.Stat(path)
			if statErr != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("directory permissions %s=%v err=%v", path, info.Mode().Perm(), statErr)
			}
		}
		info, statErr := os.Stat(filepath.Join(root, ".haowork", "core-bridge", "instance.lock"))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("lock permissions=%v err=%v", info.Mode().Perm(), statErr)
		}
	}
}

func TestInstanceLockRejectsSymlinkedGovernanceDirectory(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".haowork")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := corebridge.AcquireInstanceLock(root); !errors.Is(err, corebridge.ErrUnsafeInstanceLockPath) {
		t.Fatalf("AcquireInstanceLock() error=%v", err)
	}
}

func TestInstanceLockRejectsSecondInstanceAfterLockPathReplacement(t *testing.T) {
	root := t.TempDir()
	first, err := corebridge.AcquireInstanceLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	lockPath := filepath.Join(root, ".haowork", "core-bridge", "instance.lock")
	displaced := lockPath + ".displaced"
	if err := os.Rename(lockPath, displaced); err == nil {
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	second, err := corebridge.AcquireInstanceLock(root)
	if second != nil {
		second.Close()
	}
	if !errors.Is(err, corebridge.ErrInstanceAlreadyRunning) {
		t.Fatalf("replacement allowed second instance: lock=%#v err=%v", second, err)
	}
}

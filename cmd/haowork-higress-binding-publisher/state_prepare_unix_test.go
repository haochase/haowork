//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrepareStateDirectorySetsPrivatePublisherOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("state ownership preparation requires root")
	}
	directory := filepath.Join(t.TempDir(), "publisher-state")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "managed-consumers.json")
	if err := prepareStateDirectory(statePath, publisherUID, publisherGID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("state directory ownership is unavailable")
	}
	if info.Mode().Perm() != 0o700 || int(stat.Uid) != publisherUID || int(stat.Gid) != publisherGID {
		t.Fatalf("state directory mode/owner = %o/%d:%d", info.Mode().Perm(), stat.Uid, stat.Gid)
	}
	if err := prepareStateDirectory(statePath, publisherUID, publisherGID); err != nil {
		t.Fatalf("idempotent preparation failed: %v", err)
	}
}

//go:build !windows

package scm

import (
	"os"
	"testing"
)

func createDirectoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}
}

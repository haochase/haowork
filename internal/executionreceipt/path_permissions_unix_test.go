//go:build !windows

package executionreceipt

import (
	"fmt"
	"os"
	"testing"
)

func assertRestrictedPermissions(t *testing.T, path string, directory bool) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := os.FileMode(0o600)
	if directory {
		want = 0o700
	}
	if info.Mode().Perm()&^want != 0 {
		t.Fatalf("%s permissions = %04o, wider than %04o", path, info.Mode().Perm(), want)
	}
}

func assertTrustRootPermissions(t *testing.T, path string) {
	t.Helper()
	assertRestrictedPermissions(t, path, true)
}

func projectRootPermissionSnapshot(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%04o", info.Mode().Perm())
}

func symlinkPrivilegeUnavailable(error) bool { return false }

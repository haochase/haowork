//go:build !windows

package transferhost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOwnerOnlyFileRejectsGroupReadableMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broad")
	if err := os.WriteFile(path, []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(path); err == nil {
		t.Fatal("ValidateOwnerOnlyFile() accepted group-readable file")
	}
}

func secureOwnerOnlyForTest(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

package transferhost

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOwnerOnlyFileRejectsDirectory(t *testing.T) {
	if err := ValidateOwnerOnlyFile(t.TempDir()); err == nil {
		t.Fatal("ValidateOwnerOnlyFile() accepted a directory")
	}
}

func TestValidateOwnerOnlyFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateOwnerOnlyFile(link); err == nil {
		t.Fatal("ValidateOwnerOnlyFile() accepted a symlink")
	}
}

func TestProtectOwnerOnlyFileMakesExistingFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := ProtectOwnerOnlyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerOnlyFile(path); err != nil {
		t.Fatalf("protected file validation = %v", err)
	}
}

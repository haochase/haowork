//go:build !windows

package executionreceipt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenUnixDirectoryChainRejectsAncestorSymlink(t *testing.T) {
	base := t.TempDir()
	realAncestor := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(realAncestor, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	linkedAncestor := filepath.Join(base, "linked")
	if err := os.Symlink(realAncestor, linkedAncestor); err != nil {
		t.Fatal(err)
	}
	file, err := openUnixDirectoryChain(filepath.Join(linkedAncestor, "project"))
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, ErrUnsafeStorePath) {
		t.Fatalf("openUnixDirectoryChain() error = %v, want ancestor symlink rejection", err)
	}
}

func TestOpenUnixDirectoryChainHandlesFilesystemRoot(t *testing.T) {
	file, err := openUnixDirectoryChain(string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("filesystem root handle is not a directory")
	}
}

func TestOpenUnixDirectoryAtSurvivesAncestorReplacement(t *testing.T) {
	base := t.TempDir()
	ancestor := filepath.Join(base, "ancestor")
	project := filepath.Join(ancestor, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorFile, err := openUnixDirectoryChain(ancestor)
	if err != nil {
		t.Fatal(err)
	}
	defer ancestorFile.Close()
	original := ancestor + "-original"
	if err := os.Rename(ancestor, original); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ancestor, "project"), 0o700); err != nil {
		t.Fatal(err)
	}
	projectFile, err := openUnixDirectoryAt(int(ancestorFile.Fd()), "project", project)
	if err != nil {
		t.Fatal(err)
	}
	defer projectFile.Close()
	trustRoot, err := openUnixDirectoryAt(int(projectFile.Fd()), ".haowork", filepath.Join(project, ".haowork"))
	if err != nil {
		t.Fatal(err)
	}
	defer trustRoot.Close()
	if _, err := os.Stat(filepath.Join(ancestor, "project", ".haowork")); !os.IsNotExist(err) {
		t.Fatalf("replacement ancestor was touched: %v", err)
	}
	if info, err := os.Stat(filepath.Join(original, "project", ".haowork")); err != nil || !info.IsDir() {
		t.Fatalf("anchored original trust root = %#v, %v", info, err)
	}
}

//go:build windows

package changes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMapWindowsOpenErrorRejectsReparsePoint(t *testing.T) {
	err := mapWindowsOpenError(windows.STATUS_REPARSE_POINT_ENCOUNTERED)
	if !errors.Is(err, errRefusingSymbolicLink) {
		t.Fatalf("mapWindowsOpenError() error = %v, want symbolic link rejection", err)
	}
}

func TestSymlinkSetupRecognizesWindowsPrivilegeNotHeld(t *testing.T) {
	if !symlinkPrivilegeUnavailable(windows.ERROR_PRIVILEGE_NOT_HELD) {
		t.Fatal("ERROR_PRIVILEGE_NOT_HELD must skip symbolic-link setup")
	}
}

func TestReadFileNoFollowReadsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular.txt")
	if err := os.WriteFile(path, []byte("regular content"), 0o600); err != nil {
		t.Fatal(err)
	}

	contents, err := readFileNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "regular content" {
		t.Fatalf("readFileNoFollow() = %q, want regular content", contents)
	}
}

func TestReadFileNoFollowRejectsIntermediateDirectoryReparsePoint(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "regular.txt"), []byte("outside workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-directory")
	createSymbolicLink(t, target, link)

	_, err := readFileNoFollow(filepath.Join(link, "regular.txt"))
	if !errors.Is(err, errRefusingSymbolicLink) {
		t.Fatalf("readFileNoFollow() error = %v, want intermediate reparse point rejection", err)
	}
}

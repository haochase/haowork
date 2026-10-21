//go:build windows

package executionreceipt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOpenWindowsObjectAnchorsProjectRoot(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	projectFile, err := openWindowsObject(0, windowsNTObjectPath(project), project, true, windows.FILE_OPEN)
	if err != nil {
		t.Fatal(err)
	}
	defer projectFile.Close()
	original := filepath.Join(parent, "project-original")
	if err := os.Rename(project, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	trustRoot, err := openWindowsObject(windows.Handle(projectFile.Fd()), ".haowork", filepath.Join(project, ".haowork"), true, windows.FILE_OPEN_IF)
	if err != nil {
		t.Fatal(err)
	}
	defer trustRoot.Close()
	if _, err := os.Stat(filepath.Join(project, ".haowork")); !os.IsNotExist(err) {
		t.Fatalf("replacement project root was touched: %v", err)
	}
	if info, err := os.Stat(filepath.Join(original, ".haowork")); err != nil || !info.IsDir() {
		t.Fatalf("anchored original trust root = %#v, %v", info, err)
	}
}

func assertRestrictedPermissions(t *testing.T, path string, _ bool) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsAllowedSIDs(t, path, []*windows.SID{user.User.Sid})
}

func assertTrustRootPermissions(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	assertWindowsAllowedSIDs(t, path, []*windows.SID{user.User.Sid, system, administrators})
}

func assertWindowsAllowedSIDs(t *testing.T, path string, allowed []*windows.SID) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s DACL inherits permissions", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatalf("%s has no DACL", path)
	}
	if int(dacl.AceCount) != len(allowed) {
		t.Fatalf("%s DACL ACE count = %d, want %d", path, dacl.AceCount, len(allowed))
	}
	read := windows.ACCESS_MASK(windows.FILE_READ_DATA | windows.FILE_READ_ATTRIBUTES)
	write := windows.ACCESS_MASK(windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_ATTRIBUTES)
	seen := make([]bool, len(allowed))
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("%s ACE type = %d, want allow", path, ace.Header.AceType)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		match := -1
		for allowedIndex, sid := range allowed {
			if sid.Equals(aceSID) {
				match = allowedIndex
				break
			}
		}
		if match < 0 || seen[match] {
			t.Fatalf("%s grants unexpected or duplicate SID %s", path, aceSID.String())
		}
		seen[match] = true
		if ace.Mask&read != read || ace.Mask&write != write {
			t.Fatalf("%s allowed SID lacks read/write permissions: %#x", path, ace.Mask)
		}
	}
}

func projectRootPermissionSnapshot(t *testing.T, path string) string {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.String()
}

func symlinkPrivilegeUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD)
}

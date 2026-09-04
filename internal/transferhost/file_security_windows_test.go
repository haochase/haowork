//go:build windows

package transferhost

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidateOwnerOnlyFileRejectsEveryoneReadAccess(t *testing.T) {
	root := t.TempDir()
	privatePath := root + `\private.pem`
	publicPath := root + `\public.pem`
	if err := GenerateKeyPair(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(privatePath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := windows.StringToSid("S-1-1-0")
	if err != nil {
		t.Fatal(err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, dacl)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(privatePath, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}

	if err := ValidateOwnerOnlyFile(privatePath); err == nil {
		t.Fatal("ValidateOwnerOnlyFile() accepted Everyone read access")
	}
}

func secureOwnerOnlyForTest(t *testing.T, path string) {
	t.Helper()
	if err := secureOwnerOnly(path); err != nil {
		t.Fatal(err)
	}
}

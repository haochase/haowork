package executionreceipt

import "testing"

func TestValidAssignmentIDAcceptsOnlyCanonicalTransportIDs(t *testing.T) {
	valid := "ASN-46fc8933c009dd44765c6f4b01aeaf9d"
	if !ValidAssignmentID(valid) {
		t.Fatalf("ValidAssignmentID(%q) = false", valid)
	}
	for _, invalid := range []string{
		"ASN-46FC8933C009DD44765C6F4B01AEAF9D",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9dd",
		"ASG-46fc8933c009dd44765c6f4b01aeaf9d",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9g",
	} {
		if ValidAssignmentID(invalid) {
			t.Fatalf("ValidAssignmentID(%q) = true", invalid)
		}
	}
}

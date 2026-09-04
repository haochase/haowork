package physicalacceptance

import (
	"strings"
	"testing"
)

func TestEvaluateDirectUSBCompleteEvidence(t *testing.T) {
	result := Evaluate(completeEvidence("direct-usb"))
	if result.Status != StatusPhysicalVerified || len(result.Reasons) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestEvaluateClassifiesAdminAndPathReplaySeparately(t *testing.T) {
	for _, test := range []struct {
		transport string
		status    string
	}{
		{transport: "ssh-admin-channel", status: StatusSSHAdminE2E},
		{transport: "usb-path-replay", status: StatusUSBPathReplay},
	} {
		result := Evaluate(completeEvidence(test.transport))
		if result.Status != test.status {
			t.Fatalf("transport=%q status=%q, want %q", test.transport, result.Status, test.status)
		}
	}
}

func TestEvaluateBlocksIncompleteOrUnsafeEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Evidence)
		want   string
		part   string
	}{
		{name: "missing stage", mutate: func(value *Evidence) { value.Stages = value.Stages[:3] }, want: StatusBlocked, part: "four handoff stages"},
		{name: "wrong direction", mutate: func(value *Evidence) { value.Stages[1].SourceEnvironmentID = "internal" }, want: StatusBlocked, part: "direction"},
		{name: "network open", mutate: func(value *Evidence) { value.Network.PublicToInternalBlocked = false }, want: StatusBlocked, part: "business network"},
		{name: "unverified hash", mutate: func(value *Evidence) { value.Stages[2].Verified = false }, want: StatusBlocked, part: "verification"},
		{name: "unknown transport", mutate: func(value *Evidence) { value.Transport = "scp" }, want: StatusBlocked, part: "transport"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := completeEvidence("direct-usb")
			test.mutate(&evidence)
			result := Evaluate(evidence)
			if result.Status != test.want || !containsReason(result.Reasons, test.part) {
				t.Fatalf("result = %#v, want status %q and reason containing %q", result, test.want, test.part)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"schema_version":1,"transfer_id":"XFR","transport":"direct-usb","stages":[],"network":{},"unknown":true}`, `{"schema_version":1} {}`} {
		if _, err := Load(strings.NewReader(raw)); err == nil {
			t.Fatalf("Load accepted invalid evidence: %s", raw)
		}
	}
}

func completeEvidence(transport string) Evidence {
	return Evidence{
		SchemaVersion: 1,
		TransferID:    "XFR-PHYSICAL-001",
		Transport:     transport,
		Stages: []StageEvidence{
			{Name: "public-export", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", ArtifactSHA256: strings.Repeat("a", 64), Verified: true, Transport: transport},
			{Name: "internal-import", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", ArtifactSHA256: strings.Repeat("a", 64), Verified: true, Transport: transport},
			{Name: "internal-return", SourceEnvironmentID: "internal", TargetEnvironmentID: "public", ArtifactSHA256: strings.Repeat("b", 64), Verified: true, Transport: transport},
			{Name: "public-merge", SourceEnvironmentID: "internal", TargetEnvironmentID: "public", ArtifactSHA256: strings.Repeat("b", 64), Verified: true, Transport: transport},
		},
		Network: NetworkEvidence{PublicToInternalBlocked: true, InternalToPublicBlocked: true, PublicCoreHealthy: true, InternalCoreHealthy: true},
	}
}

func containsReason(reasons []string, part string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, part) {
			return true
		}
	}
	return false
}

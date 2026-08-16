package patchskills

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/haochase/haowork/internal/skillruntime"
)

func TestCrossZoneAdaptersOnlyProduceSignedRequests(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"advisory", "mirror", "patch", "audit"} {
		t.Run(name, func(t *testing.T) {
			adapter := Adapter{Name: name, Signer: NewEd25519Signer("key-1", private)}
			invocation := skillruntime.Invocation{MissionID: "MSN-1", EnvironmentID: "public", SkillName: name, SkillVersion: "1.0.0", LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 1, Scope: []string{"src/**"}, Input: json.RawMessage(`{"request":"only"}`)}
			adapter.BuildAgentID, adapter.VerifyAgentID = "AGT-BUILD", "AGT-VERIFY"
			if name == "patch" {
				invocation.Input = json.RawMessage(`{"paths":["src/main.go"]}`)
			}
			if name == "audit" {
				invocation.LogicalActorID = "AGT-VERIFY"
				adapter.AuditCommand, adapter.WorkspaceDigest, adapter.ArtifactHash = "go test ./...", "workspace", "artifact"
			}
			output, _, err := adapter.Invoke(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			var request SignedRequest
			if err := json.Unmarshal(output, &request); err != nil {
				t.Fatal(err)
			}
			if err := request.Verify(public); err != nil {
				t.Fatalf("signed request verification failed: %v", err)
			}
			if request.Request.MissionID != "MSN-1" || request.Request.SkillName != name {
				t.Fatalf("request = %#v", request.Request)
			}
		})
	}
}

func TestPatchCannotWriteOutsideMissionScope(t *testing.T) {
	if err := ValidatePatchScope([]string{"src/**"}, []string{"src/main.go"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePatchScope([]string{"src/**"}, []string{"../secret.txt"}); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := ValidatePatchScope([]string{"src/**"}, []string{"README.md"}); err == nil {
		t.Fatal("outside mission scope was accepted")
	}
}

func TestPatchAdapterBindsIdentityAndValidatesInvocationScope(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Name: "patch", Signer: NewEd25519Signer("key-1", private), BuildAgentID: "AGT-BUILD", VerifyAgentID: "AGT-VERIFY"}
	invocation := skillruntime.Invocation{MissionID: "MSN-1", EnvironmentID: "internal", SkillName: "patch", SkillVersion: "1.0.0", LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 4, Scope: []string{"src/**"}, Input: json.RawMessage(`{"paths":["src/main.go"]}`)}
	output, _, err := adapter.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	var signed SignedRequest
	if err := json.Unmarshal(output, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.Request.LogicalActorID != "AGT-BUILD" || signed.Request.RuntimeBindingRevision != 4 || signed.Request.AgentFunction != "build" || signed.Request.BuildAgentID != "AGT-BUILD" || signed.Request.VerifyAgentID != "AGT-VERIFY" {
		t.Fatalf("request binding = %#v", signed.Request)
	}
	invocation.Input = json.RawMessage(`{"paths":["../escape"]}`)
	if _, _, err := adapter.Invoke(context.Background(), invocation); err == nil {
		t.Fatal("outside patch scope accepted")
	}
}

func TestAuditAdapterRequiresIndependentIdentityAndEmitsAuditFacts(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	adapter := Adapter{Name: "audit", Signer: NewEd25519Signer("key-1", private), BuildAgentID: "AGT-BUILD", VerifyAgentID: "AGT-VERIFY", AuditCommand: "go test ./...", AuditExitCode: 0, WorkspaceDigest: "workspace-sha", ArtifactHash: "artifact-sha"}
	invocation := skillruntime.Invocation{MissionID: "MSN-1", EnvironmentID: "internal", SkillName: "audit", SkillVersion: "1.0.0", LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 2, Scope: []string{"src/**"}, Input: json.RawMessage(`{}`)}
	if _, _, err := adapter.Invoke(context.Background(), invocation); err == nil {
		t.Fatal("build identity performed audit")
	}
	invocation.LogicalActorID = "AGT-VERIFY"
	output, _, err := adapter.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	var signed SignedRequest
	if err := json.Unmarshal(output, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.Request.Audit.Command != "go test ./..." || signed.Request.Audit.ExitCode != 0 || signed.Request.Audit.WorkspaceDigest != "workspace-sha" || signed.Request.Audit.ArtifactHash != "artifact-sha" {
		t.Fatalf("audit = %#v", signed.Request.Audit)
	}
}

func TestAuditRequiresDifferentLogicalAgentFromBuild(t *testing.T) {
	if err := ValidateAudit("AGT-BUILD", "AGT-BUILD"); err == nil {
		t.Fatal("audit accepted build logical identity")
	}
	if err := ValidateAudit("AGT-BUILD", "AGT-VERIFY"); err != nil {
		t.Fatal(err)
	}
}

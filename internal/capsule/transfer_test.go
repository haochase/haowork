package capsule_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
)

func TestCapsuleTransferFacadeExportsSignedArchive(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = capsule.ExportTransfer(capsule.TransferExportInput{Manifest: capsule.TransferManifest{
		ProtocolVersion: capsule.TransferProtocolVersion, TransferID: "XFR-001", ProjectID: "PRJ-001", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", GoalVersion: 1,
		ContextID: "CTX-001", ContextHash: "context", LeaseID: "LSE-001", Scope: []string{"src/**"}, Skills: []capsule.SkillRef{{Name: "patch", Version: "1.0.0"}},
		Agents: []capsule.RuntimeHistory{{LogicalActorID: "AGT-BUILD", Bindings: []capsule.RuntimeBinding{{LogicalActorID: "AGT-BUILD", Revision: 1, EnvironmentID: "public", RuntimePrincipalID: "build", AgentTeamsInstanceID: "team"}}}}, TraceIDs: []string{"TRC-001"}, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, Signer: transferSigner("key-1", private), ProvenanceVerifier: capsule.TransferProvenanceVerifierFunc(func(context.Context, capsule.TransferEntry) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
}

func transferSigner(keyID string, private ed25519.PrivateKey) capsule.TransferSigner {
	return capsule.NewTransferEd25519Signer(keyID, private)
}

package e2e_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
)

func TestOfflineHandoffRejectsUnsignedExpiredOrWrongEnvironmentCapsule(t *testing.T) {
	publicKey, privateKey := offlineKeyPair(t)
	archive := offlineExport(t, privateKey, offlineManifest("XFR-OFFLINE-001", "public", "internal", offlineNow.Add(time.Hour)))
	internal := offlineImportService("internal", "public-key", publicKey, offlineNow)

	broken := append([]byte(nil), archive...)
	broken[len(broken)-1] ^= 1
	if _, err := internal.PreviewImport(context.Background(), broken); err == nil {
		t.Fatal("unsigned capsule was accepted")
	}

	expiredManifest := offlineManifest("XFR-OFFLINE-EXPIRED", "public", "internal", offlineNow.Add(-time.Minute))
	expiredManifest.CreatedAt = offlineNow.Add(-2 * time.Hour)
	expired := offlineExport(t, privateKey, expiredManifest)
	if _, err := internal.PreviewImport(context.Background(), expired); err == nil {
		t.Fatal("expired capsule was accepted")
	}

	wrongTarget := offlineImportService("restricted", "public-key", publicKey, offlineNow)
	if _, err := wrongTarget.PreviewImport(context.Background(), archive); err == nil {
		t.Fatal("capsule for another environment was accepted")
	}
}

func TestOfflineImportRebindsLogicalIdentityToInternalRuntime(t *testing.T) {
	publicKey, privateKey := offlineKeyPair(t)
	archive := offlineExport(t, privateKey, offlineManifest("XFR-OFFLINE-REBIND", "public", "internal", offlineNow.Add(time.Hour)))
	writer := &offlineImportWriter{}
	service := offlineImportService("internal", "public-key", publicKey, offlineNow)
	service.Writer = writer

	preview, err := service.PreviewImport(context.Background(), archive)
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.RebindRequired[0].RuntimePrincipalID; got != "runtime-internal-build" {
		t.Fatalf("target runtime principal = %q", got)
	}
	if err := service.ApplyImport(context.Background(), preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: offlineOwner}); err != nil {
		t.Fatal(err)
	}
	if writer.binding.LogicalActorID != "AGT-OFFLINE-BUILD" || writer.binding.RuntimePrincipalID != "runtime-internal-build" || writer.binding.TargetEnvironmentID != "internal" {
		t.Fatalf("internal rebind = %#v", writer.binding)
	}
}

func TestOfflineReturnContainsOnlyApprovedDeltaAndEvidence(t *testing.T) {
	_, privateKey := offlineKeyPair(t)
	base := offlineManifest("XFR-OFFLINE-RETURN", "public", "internal", offlineNow.Add(time.Hour))
	approved := offlineEntry(transfer.EntryGitDiff, "git/diff/approved.json", []byte(`{"patch":"approved"}`))
	unapproved := offlineEntry(transfer.EntryGitDiff, "git/diff/unapproved.json", []byte(`{"patch":"unapproved"}`))
	service := transfer.Service{
		ReturnSigner:       transfer.NewEd25519Signer("internal-key", privateKey),
		ApprovalVerifier:   transfer.ApprovalVerifierFunc(func(context.Context, string, string) error { return nil }),
		ProvenanceVerifier: transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil }),
	}
	request := transfer.ReturnRequest{
		Base:                base,
		Current:             transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-OFFLINE", Scope: []string{"src/**"}},
		Candidate:           transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-OFFLINE", Scope: []string{"src/**"}},
		Changes:             []transfer.ApprovedChange{{Entry: approved}, {Entry: unapproved}},
		ApprovedEntryHashes: []string{transfer.EntryApprovalHash(approved)},
	}
	request.Approval = transfer.ReturnApproval{ID: "APR-OFFLINE", PayloadSHA256: transfer.ReturnApprovalHash(request)}
	delta, err := service.BuildReturn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Entries) != 1 || string(delta.Entries[0].Data) != string(approved.Data) {
		t.Fatalf("returned entries = %#v", delta.Entries)
	}
	if delta.Manifest.SourceEnvironmentID != "internal" || delta.Manifest.TargetEnvironmentID != "public" || len(delta.Archive) == 0 {
		t.Fatalf("return delta = %#v", delta)
	}
}

func TestOfflinePublicMergeRequiresOwnerApprovalAndPreservesConflictBranches(t *testing.T) {
	publicKey, publicPrivateKey := offlineKeyPair(t)
	internalKey, internalPrivateKey := offlineKeyPair(t)
	base := offlineManifest("XFR-OFFLINE-MERGE", "public", "internal", offlineNow.Add(time.Hour))
	change := offlineEntry(transfer.EntryGitDiff, "git/diff/fix.json", []byte(`{"patch":"fix"}`))
	internal := transfer.Service{
		ReturnSigner:       transfer.NewEd25519Signer("internal-key", internalPrivateKey),
		ApprovalVerifier:   transfer.ApprovalVerifierFunc(func(context.Context, string, string) error { return nil }),
		ProvenanceVerifier: transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil }),
	}
	request := transfer.ReturnRequest{Base: base, Current: transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-OFFLINE", Scope: []string{"src/**"}}, Candidate: transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-OFFLINE", Scope: []string{"src/**"}}, Changes: []transfer.ApprovedChange{{Entry: change}}, ApprovedEntryHashes: []string{transfer.EntryApprovalHash(change)}}
	request.Approval = transfer.ReturnApproval{ID: "APR-MERGE", PayloadSHA256: transfer.ReturnApprovalHash(request)}
	returned, err := internal.BuildReturn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writer := &offlineImportWriter{}
	public := offlineImportService("public", "internal-key", internalKey, offlineNow)
	public.Writer = writer
	preview, err := public.PreviewImport(context.Background(), returned.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(returned.Conflicts) == 0 {
		t.Fatal("return conflict classification was lost")
	}
	lead := model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}
	if err := public.ApplyImport(context.Background(), preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: lead}); err == nil {
		t.Fatal("public merge accepted a non-owner approval")
	}
	if writer.calls != 0 {
		t.Fatal("public merge wrote a binding before owner approval")
	}
	if err := public.ApplyImport(context.Background(), preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: offlineOwner}); err != nil {
		t.Fatal(err)
	}
	if writer.binding.RuntimePrincipalID != "runtime-public-build" || writer.binding.TargetEnvironmentID != "public" {
		t.Fatalf("public merge binding = %#v", writer.binding)
	}
	if len(publicKey) != ed25519.PublicKeySize || len(publicPrivateKey) != ed25519.PrivateKeySize {
		t.Fatal("public signing fixture is invalid")
	}
}

var offlineNow = time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC)
var offlineOwner = model.Actor{ID: "USR-OFFLINE-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}

type offlineImportWriter struct {
	calls   int
	binding transfer.RebindCandidate
}

func (writer *offlineImportWriter) CommitImport(_ context.Context, binding transfer.RebindCandidate, _ string, _ model.Actor) error {
	writer.calls++
	writer.binding = binding
	return nil
}

func offlineKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func offlineExport(t *testing.T, private ed25519.PrivateKey, manifest transfer.Manifest) []byte {
	t.Helper()
	archive, err := transfer.ExportBytes(transfer.ExportInput{
		Manifest:           manifest,
		Entries:            []transfer.Entry{offlineEntry(transfer.EntryTrace, "trace/TRC-OFFLINE.json", []byte(`{"trace":"offline"}`))},
		Signer:             transfer.NewEd25519Signer("public-key", private),
		ProvenanceVerifier: transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func offlineImportService(environmentID, keyID string, key ed25519.PublicKey, now time.Time) transfer.Service {
	return transfer.Service{
		TargetEnvironmentID: environmentID,
		PublicKeys:          map[string]ed25519.PublicKey{keyID: key},
		Now:                 func() time.Time { return now },
		RuntimeBindingResolver: transfer.RuntimeBindingResolverFunc(func(_ context.Context, logicalID, targetEnvironmentID string) (model.RuntimeBinding, error) {
			return model.RuntimeBinding{LogicalActorID: logicalID, Revision: 7, EnvironmentID: targetEnvironmentID, RuntimePrincipalID: "runtime-" + targetEnvironmentID + "-build", AgentTeamsInstanceID: "agentteams-" + targetEnvironmentID}, nil
		}),
	}
}

func offlineManifest(id, source, target string, expiresAt time.Time) transfer.Manifest {
	return transfer.Manifest{
		ProtocolVersion: transfer.ProtocolVersion, TransferID: id, ProjectID: "PRJ-OFFLINE", SourceEnvironmentID: source, TargetEnvironmentID: target, GoalVersion: 1,
		MissionIDs: []string{"MSN-OFFLINE"}, GovernanceEventIDs: []string{"EVT-OFFLINE"}, TraceIDs: []string{"TRC-OFFLINE"}, TaskIDs: []string{"TSK-OFFLINE"}, WorkItemIDs: []string{"WKI-OFFLINE"}, RunIDs: []string{"RUN-OFFLINE"},
		ContextID: "CTX-OFFLINE", ContextHash: "context-offline", LeaseID: "LSE-OFFLINE", Scope: []string{"src/**"}, Skills: []transfer.SkillRef{{Name: "patch", Version: "1.0.0"}},
		Agents:    []transfer.RuntimeHistory{{LogicalActorID: "AGT-OFFLINE-BUILD", Bindings: []model.RuntimeBinding{{LogicalActorID: "AGT-OFFLINE-BUILD", Revision: 1, EnvironmentID: source, RuntimePrincipalID: "runtime-" + source + "-build", AgentTeamsInstanceID: "agentteams-" + source}}}},
		CreatedAt: offlineNow, ExpiresAt: expiresAt,
	}
}

func offlineEntry(kind transfer.EntryType, path string, data []byte) transfer.Entry {
	digest := sha256.Sum256(data)
	source := "governance-ledger"
	if kind == transfer.EntryTrace {
		source = "trace-ledger"
	} else if kind == transfer.EntryGitDiff {
		source = "git"
	}
	return transfer.Entry{Type: kind, Path: path, Data: data, Provenance: transfer.EntryProvenance{Source: source, SHA256: hex.EncodeToString(digest[:])}}
}

var _ = json.Valid

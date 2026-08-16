package transfer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

func TestExportIsDeterministicAndExcludesSecretsChatAndPrivateMemory(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := exportFixture(private)
	first, err := ExportBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExportBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("export is not deterministic")
	}
	for _, forbidden := range []string{"token-like-secret", "full Matrix message", "private memory", "Bearer eyJ", "sk-live-"} {
		if bytes.Contains(first, []byte(forbidden)) {
			t.Fatalf("export contains %q", forbidden)
		}
	}
}

func TestExportRejectsUnknownAndSensitiveEntryTypesWithoutKeywordFiltering(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []Entry{
		{Type: EntryUnknown, Path: "facts/unknown.json", Data: []byte(`{"safe":"text"}`)},
		{Type: EntryChatTranscript, Path: "facts/chat.json", Data: []byte(`{"safe":"text"}`)},
		{Type: EntryPrivateMemory, Path: "facts/memory.json", Data: []byte(`{"safe":"text"}`)},
		{Type: EntryCredential, Path: "facts/credential.json", Data: []byte(`{"safe":"text"}`)},
	} {
		input := exportFixture(private)
		input.Entries = []Entry{entry}
		if _, err := ExportBytes(input); err == nil {
			t.Fatalf("entry type %q was exported", entry.Type)
		}
	}
}

func TestExportSignsWithInjectedSignerAndNeverStoresPrivateKey(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := exportFixture(private)
	if input.Signer == nil {
		t.Fatal("fixture must inject signer")
	}
	archive, err := ExportBytes(input)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, private) {
		t.Fatal("archive contains private key")
	}
}

func TestExportRejectsSensitiveJSONMasqueradingAsTraceWithoutTrustedProvenance(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	input := exportFixture(private)
	input.Entries = []Entry{verifiedEntry(EntryTrace, "trace/TRC-001.json", []byte(`{"token":"not-a-trace"}`))}
	input.ProvenanceVerifier = ProvenanceVerifierFunc(func(context.Context, Entry) error {
		return errors.New("trace entry is absent from the trace ledger")
	})
	if _, err := ExportBytes(input); err == nil {
		t.Fatal("sensitive JSON masquerading as a trace was exported")
	}
}

func TestTraceStoreProvenanceVerifierOnlyExportsRecordedTraceBytes(t *testing.T) {
	store := trace.New(t.TempDir())
	record, err := store.AppendIdempotent(context.Background(), trace.Envelope{ID: "TRC-001", MissionID: "MSN-001", GovernanceTaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 1, EnvironmentID: "internal", AgentTeamsInstanceID: "TEAM-001", SourceEventID: "SRC-001", SourceEventType: "trace.invocation.started", Status: "started", StartedAt: fixedNow()})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	entry := verifiedEntry(EntryTrace, "trace/TRC-001.json", data)
	verifier := TraceStoreProvenanceVerifier{Store: store}
	if err := verifier.VerifyProvenance(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	entry.Data = []byte(`{"token":"masquerading trace"}`)
	if err := verifier.VerifyProvenance(context.Background(), entry); err == nil {
		t.Fatal("unrecorded trace bytes were accepted")
	}
}

func TestImportPreviewDoesNotWriteFilesEventsOrBindings(t *testing.T) {
	archive := archiveFixture(t)
	writer := &recordingWriter{}
	service := Service{TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey}, Writer: writer, Now: fixedNow, RuntimeBindingResolver: RuntimeBindingResolverFunc(testBindingResolver)}
	preview, err := service.PreviewImport(context.Background(), archive.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if preview.RebindRequired[0].LogicalActorID != "AGT-BUILD" || writer.calls != 0 {
		t.Fatalf("preview = %#v, writes = %d", preview, writer.calls)
	}
}

func TestImportPreviewUsesTargetRuntimeBindingRatherThanSourceBinding(t *testing.T) {
	archive := archiveFixture(t)
	service := Service{
		TargetEnvironmentID: "internal",
		PublicKeys:          map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey},
		Now:                 fixedNow,
		RuntimeBindingResolver: RuntimeBindingResolverFunc(func(_ context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
			return model.RuntimeBinding{LogicalActorID: logicalID, Revision: 9, EnvironmentID: environmentID, RuntimePrincipalID: "target-principal", AgentTeamsInstanceID: "TEAM-TARGET"}, nil
		}),
	}
	preview, err := service.PreviewImport(context.Background(), archive.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	candidate := preview.RebindRequired[0]
	if candidate.RuntimePrincipalID != "target-principal" || candidate.AgentTeamsInstanceID != "TEAM-TARGET" || candidate.RuntimePrincipalID == "build-public" || candidate.AgentTeamsInstanceID == "TEAM-PUBLIC" {
		t.Fatalf("rebind candidate copied source runtime binding: %#v", candidate)
	}
}

func TestImportPreviewRejectsMissingTargetRuntimeBindingResolver(t *testing.T) {
	archive := archiveFixture(t)
	service := Service{TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey}, Now: fixedNow}
	if _, err := service.PreviewImport(context.Background(), archive.Bytes); err == nil {
		t.Fatal("preview accepted without a target runtime binding resolver")
	}
}

func TestImportRejectsSignatureHashBaselineGoalAndCapabilityMismatch(t *testing.T) {
	archive := archiveFixture(t)
	service := Service{TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey}, Now: fixedNow, ExpectedGoalVersion: 2, RequiredSkills: map[string]string{"patch": "1.0.0"}}
	if _, err := service.PreviewImport(context.Background(), archive.Bytes); err == nil {
		t.Fatal("goal mismatch accepted")
	}
	service.ExpectedGoalVersion = 1
	service.RequiredSkills = map[string]string{"patch": "2.0.0"}
	if _, err := service.PreviewImport(context.Background(), archive.Bytes); err == nil {
		t.Fatal("unknown skill version accepted")
	}
	broken := append([]byte(nil), archive.Bytes...)
	broken[len(broken)-1] ^= 1
	service.RequiredSkills = map[string]string{"patch": "1.0.0"}
	if _, err := service.PreviewImport(context.Background(), broken); err == nil {
		t.Fatal("broken archive accepted")
	}
}

func TestConfirmedImportRebindsLogicalIdentityWithNewRevision(t *testing.T) {
	archive := archiveFixture(t)
	writer := &recordingWriter{revision: 3}
	service := Service{TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey}, Writer: writer, Now: fixedNow, RuntimeBindingResolver: RuntimeBindingResolverFunc(testBindingResolver)}
	preview, err := service.PreviewImport(context.Background(), archive.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyImport(context.Background(), preview, Approval{PreviewHash: preview.PreviewHash, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}); err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || writer.binding.NewRevision != 4 || writer.eventType != "capsule.imported" {
		t.Fatalf("writer = %#v", writer)
	}
}

func fixedNow() time.Time { return time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC) }

func exportFixture(private ed25519.PrivateKey) ExportInput {
	return ExportInput{
		Manifest: Manifest{
			ProtocolVersion: ProtocolVersion, TransferID: "XFR-001", ProjectID: "PRJ-001", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", GoalVersion: 1,
			MissionIDs: []string{"MSN-001"}, GovernanceEventIDs: []string{"EVT-001"}, TraceIDs: []string{"TRC-001"}, TaskIDs: []string{"TSK-001"}, WorkItemIDs: []string{"WKI-001"}, RunIDs: []string{"RUN-001"},
			ContextID: "CTX-001", ContextHash: "context-hash", LeaseID: "LSE-001", Scope: []string{"src/**"}, Skills: []SkillRef{{Name: "patch", Version: "1.0.0"}},
			Agents:     []RuntimeHistory{{LogicalActorID: "AGT-BUILD", Bindings: []model.RuntimeBinding{{LogicalActorID: "AGT-BUILD", Revision: 3, EnvironmentID: "public", RuntimePrincipalID: "build-public", AgentTeamsInstanceID: "TEAM-PUBLIC"}}}},
			MatrixRefs: []MatrixRef{{InstanceID: "TEAM-PUBLIC", RoomID: "!room", EventID: "$event", ContentHash: "body-hash"}}, ArtifactRefs: []ArtifactRef{{URI: "artifact://build", SHA256: "artifact-hash"}}, TraceCursor: "opaque-cursor", TraceHash: "trace-hash", RedactionPolicy: "matrix-refs-only", GitBaseline: "abc123", CreatedAt: fixedNow(), ExpiresAt: fixedNow().Add(time.Hour),
		},
		Entries: []Entry{verifiedEntry(EntryTrace, "trace/TRC-001.json", []byte(`{"trace":"allowed"}`))}, Signer: NewEd25519Signer("key-1", private), ProvenanceVerifier: ProvenanceVerifierFunc(func(context.Context, Entry) error { return nil }),
	}
}

func verifiedEntry(kind EntryType, path string, data []byte) Entry {
	digest := sha256.Sum256(data)
	source := map[EntryType]string{EntryTrace: "trace-ledger", EntryGitDiff: "git"}[kind]
	return Entry{Type: kind, Path: path, Data: data, Provenance: EntryProvenance{Source: source, SHA256: hex.EncodeToString(digest[:])}}
}

func archiveFixture(t *testing.T) Archive {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := ExportBytes(exportFixture(private))
	if err != nil {
		t.Fatal(err)
	}
	return Archive{Bytes: bytes, KeyID: "key-1", PublicKey: public}
}

type recordingWriter struct {
	calls, revision int
	binding         RebindCandidate
	eventType       string
}

func testBindingResolver(_ context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
	return model.RuntimeBinding{LogicalActorID: logicalID, Revision: 3, EnvironmentID: environmentID, RuntimePrincipalID: "target-" + logicalID, AgentTeamsInstanceID: "TEAM-TARGET"}, nil
}

func (writer *recordingWriter) CommitImport(_ context.Context, binding RebindCandidate, _ string, _ model.Actor) error {
	writer.calls++
	binding.NewRevision = writer.revision + 1
	writer.binding = binding
	writer.eventType = "capsule.imported"
	return nil
}

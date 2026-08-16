package transfer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"testing"

	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

func TestBuildReturnExportsOnlyApprovedChangesAndClassifiesSixConflicts(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{ReturnSigner: NewEd25519Signer("return-key", private), ApprovalVerifier: ApprovalVerifierFunc(func(context.Context, string, string) error { return nil }), ProvenanceVerifier: ProvenanceVerifierFunc(func(context.Context, Entry) error { return nil })}
	approved := verifiedEntry(EntryGitDiff, "git/diff/approved.patch", []byte(`{"patch":"approved"}`))
	unapproved := verifiedEntry(EntryGitDiff, "git/diff/unapproved.patch", []byte(`{"patch":"unapproved"}`))
	request := ReturnRequest{Base: exportFixture(private).Manifest, Current: ReturnState{GoalVersion: 2, LeaseID: "LSE-OTHER", Scope: []string{"src/**"}, GitBaseline: "base-other", DesignHash: "design-other", EvidenceHash: "evidence-other", Terminal: true}, Candidate: ReturnState{GoalVersion: 1, LeaseID: "LSE-001", Scope: []string{"src/**"}, GitBaseline: "abc123", DesignHash: "design-source", EvidenceHash: "evidence-source"}, Changes: []ApprovedChange{{Entry: approved}, {Entry: unapproved}}, ApprovedEntryHashes: []string{EntryApprovalHash(approved)}}
	request.Approval = ReturnApproval{ID: "APR-001", PayloadSHA256: ReturnApprovalHash(request)}
	delta, err := service.BuildReturn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Conflicts) != 6 {
		t.Fatalf("conflicts = %#v, want six", delta.Conflicts)
	}
	if len(delta.Entries) != 1 || string(delta.Entries[0].Data) != `{"patch":"approved"}` {
		t.Fatalf("entries = %#v", delta.Entries)
	}
	if len(delta.Archive) == 0 {
		t.Fatal("return archive is empty")
	}
}

func TestReturnApprovalHashBindsCompleteManifestAndEntryMetadata(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	entry := verifiedEntry(EntryGitDiff, "git/diff/approved.patch", []byte(`{"patch":"approved"}`))
	request := ReturnRequest{Base: exportFixture(private).Manifest, Changes: []ApprovedChange{{Entry: entry}}, ApprovedEntryHashes: []string{EntryApprovalHash(entry)}}
	request.Base.ParentTransferID = "XFR-PARENT"
	want := ReturnApprovalHash(request)
	for _, mutate := range []func(*ReturnRequest){
		func(value *ReturnRequest) { value.Base.ProtocolVersion = "2.0" },
		func(value *ReturnRequest) { value.Base.SourceEnvironmentID = "other-source" },
		func(value *ReturnRequest) { value.Base.TargetEnvironmentID = "other-target" },
		func(value *ReturnRequest) { value.Base.ParentTransferID = "other-parent" },
		func(value *ReturnRequest) { value.Base.Scope = []string{"other/**"} },
		func(value *ReturnRequest) { value.Base.Skills = []SkillRef{{Name: "other", Version: "9"}} },
		func(value *ReturnRequest) {
			value.ApprovedEntryHashes = []string{EntryApprovalHash(verifiedEntry(EntryGitDiff, "git/diff/replaced.patch", []byte(`{"patch":"approved"}`)))}
		},
		func(value *ReturnRequest) { value.Changes[0].Entry.Path = "git/diff/replaced.patch" },
	} {
		candidate := request
		mutate(&candidate)
		if got := ReturnApprovalHash(candidate); got == want {
			t.Fatal("approval hash did not bind a return-critical manifest or entry field")
		}
	}
}

func TestCoreTeamWriterUsesEventStoreBatchSink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/events.jsonl", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := eventstore.NewAt(dir+"/events.jsonl", dir+"/events.lock")
	writer := CoreTeamWriter{ProjectID: "PRJ-TEST", GoalVersion: 1, Appender: eventstore.NewBatchSink(store), NewEventID: sequenceIDs(), Now: fixedNow}
	if err := writer.CommitImport(context.Background(), RebindCandidate{LogicalActorID: "AGT-BUILD", NewRevision: 2, TargetEnvironmentID: "internal", RuntimePrincipalID: "build-internal", AgentTeamsInstanceID: "TEAM-INTERNAL"}, "preview", model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadAll(context.Background())
	if err != nil || len(events) != 2 || events[0].Type != "capsule.imported" || events[1].Type != "agent.runtime.bound" {
		t.Fatalf("stored events = %#v, %v", events, err)
	}
}

func TestEventStoreApprovalVerifierRequiresOwnerOrLeadApprovedEvent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/events.jsonl", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := eventstore.NewAt(dir+"/events.jsonl", dir+"/events.lock")
	payloadHash := "return-payload-hash"
	requested, err := json.Marshal(model.ApprovalRequested{Approval: model.ApprovalRequest{ID: "APR-RETURN", SubjectType: "transfer", SubjectID: "XFR-001-return", PayloadSHA256: payloadHash, RiskLevel: "L2", RequesterID: "AGT-BUILD"}})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := json.Marshal(model.ApprovalDecided{ApprovalID: "APR-RETURN", PayloadSHA256: payloadHash, Decision: "approved", DeciderID: "USR-LEAD"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []model.Event{
		{ID: "EVT-REQUEST", Type: "approval.requested", AggregateType: "approval", AggregateID: "APR-RETURN", Actor: model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}, OccurredAt: fixedNow(), Payload: requested},
		{ID: "EVT-DECIDED", Type: "approval.decided", AggregateType: "approval", AggregateID: "APR-RETURN", Actor: model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}, OccurredAt: fixedNow(), Payload: decided},
	} {
		if _, err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := (EventStoreApprovalVerifier{Events: store}).VerifyApproval(context.Background(), "APR-RETURN", payloadHash); err != nil {
		t.Fatal(err)
	}
}

func sequenceIDs() func() string {
	n := 0
	return func() string { n++; return "EVT-" + string(rune('0'+n)) }
}

func TestApplyImportRequiresOwnerBoundPreviewAndProductionWriter(t *testing.T) {
	archive := archiveFixture(t)
	appender := &eventAppender{}
	writer := CoreTeamWriter{ProjectID: "PRJ-001", GoalVersion: 1, Appender: appender, NewEventID: func() string { return "EVT-IMPORT" }, Now: fixedNow}
	service := Service{TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{archive.KeyID: archive.PublicKey}, Writer: writer, Now: fixedNow, RuntimeBindingResolver: RuntimeBindingResolverFunc(testBindingResolver)}
	preview, err := service.PreviewImport(context.Background(), archive.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyImport(context.Background(), preview, Approval{PreviewHash: preview.PreviewHash, Actor: model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}}); err == nil {
		t.Fatal("lead approval accepted")
	}
	if err := service.ApplyImport(context.Background(), preview, Approval{PreviewHash: "changed", Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}); err == nil {
		t.Fatal("wrong preview hash accepted")
	}
	if err := service.ApplyImport(context.Background(), preview, Approval{PreviewHash: preview.PreviewHash, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}); err != nil {
		t.Fatal(err)
	}
	if len(appender.events) != 2 || appender.events[0].Type != "capsule.imported" || appender.events[1].Type != "agent.runtime.bound" {
		t.Fatalf("events = %#v", appender.events)
	}
}

type eventAppender struct{ events []model.Event }

func (appender *eventAppender) AppendBatch(_ context.Context, events []model.Event) error {
	appender.events = append(appender.events, events...)
	return nil
}

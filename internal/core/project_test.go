package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/patchskills"
	"github.com/haochase/haowork/internal/skillapi"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/haochase/haowork/internal/testkit"
	"github.com/haochase/haowork/internal/transfer"
)

func TestOpenBuildsServiceFromCapsule(t *testing.T) {
	ctx := context.Background()
	root, manifest := initializeCapsule(t, ctx)
	nested := filepath.Join(root, "src", "service")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	project, err := Open(ctx, nested, Dependencies{
		IDs:   &testkit.IDs{},
		Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.Root != root {
		t.Fatalf("Project.Root = %q, want %q", project.Root, root)
	}
	if project.Manifest.ProjectID != manifest.ProjectID {
		t.Fatalf("Project.Manifest.ProjectID = %q, want %q", project.Manifest.ProjectID, manifest.ProjectID)
	}
	if project.Manifest.CurrentGoalVersion != manifest.CurrentGoalVersion {
		t.Fatalf("Project.Manifest.CurrentGoalVersion = %d, want %d", project.Manifest.CurrentGoalVersion, manifest.CurrentGoalVersion)
	}
	if project.SkillAdapters["plan"] == nil || project.SkillAdapters["patch"] == nil || project.SkillAdapters["export"] == nil {
		t.Fatalf("Project.SkillAdapters does not own the canonical local and deferred adapter bindings: %#v", project.SkillAdapters)
	}

	state, err := project.Service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != manifest.ProjectID {
		t.Fatalf("Project.Service.Status().ProjectID = %q, want %q", state.ProjectID, manifest.ProjectID)
	}
	if state.Goal.Version != manifest.CurrentGoalVersion {
		t.Fatalf("Project.Service.Status().Goal.Version = %d, want %d", state.Goal.Version, manifest.CurrentGoalVersion)
	}
}

func TestOpenConfiguresSCMOnlyForGitProjectRoot(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	project, err := Open(ctx, root, Dependencies{IDs: &testkit.IDs{Next: 100}, Clock: testkit.Clock{Value: fixedCoreTime()}})
	if err != nil {
		t.Fatal(err)
	}
	if project.SCMAvailable {
		t.Fatal("non-Git project reported SCM available")
	}
	command := exec.Command("git", "-C", root, "init")
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("git init: %v\n%s", commandErr, output)
	}
	project, err = Open(ctx, root, Dependencies{IDs: &testkit.IDs{Next: 100}, Clock: testkit.Clock{Value: fixedCoreTime()}})
	if err != nil {
		t.Fatal(err)
	}
	if !project.SCMAvailable {
		t.Fatal("Git project did not report SCM available")
	}
	if _, err := project.Service.RegisterSCM(ctx, model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}); err != nil {
		t.Fatalf("register configured SCM: %v", err)
	}
}

func TestCoreOpenFailsClosedWhenAnyOfficialDependencyIsUnavailable(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	_, err := Open(ctx, root, Dependencies{
		IDs:        &testkit.IDs{},
		Clock:      testkit.Clock{Value: fixedCoreTime()},
		AgentTeams: &agentteamsbridge.ProductionConfig{},
	})
	if err == nil {
		t.Fatal("Open() accepted incomplete official AgentTeams dependencies")
	}
}

func TestOpenInjectsConfiguredCrossZoneAuditAdapter(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	project, err := Open(ctx, root, Dependencies{
		IDs:       &testkit.IDs{},
		Clock:     testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
		CrossZone: &skillapi.CrossZoneConfig{Signer: patchskills.NewEd25519Signer("key-1", private), BuildAgentID: "AGT-BUILD", VerifyAgentID: "AGT-VERIFY", AuditCommand: "go test ./...", WorkspaceDigest: "workspace-sha", ArtifactHash: "artifact-sha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	output, _, err := project.SkillAdapters.Invoke(ctx, skillruntime.Invocation{MissionID: "MSN-001", EnvironmentID: "internal", SkillName: "audit", SkillVersion: "1.0.0", LogicalActorID: "AGT-VERIFY", RuntimeBindingRevision: 1, Scope: []string{"src/**"}, Input: json.RawMessage(`{"run":"audit"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var signed patchskills.SignedRequest
	if err := json.Unmarshal(output, &signed); err != nil || signed.Request.Audit.ArtifactHash != "artifact-sha" {
		t.Fatalf("configured audit output = %s, %v", output, err)
	}
}

func TestOpenAssemblesTransferServiceWithProductionBoundaries(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := transfer.NewEd25519Signer("key-1", private)
	ids := &testkit.IDs{}
	project, err := Open(ctx, root, Dependencies{
		IDs:   ids,
		Clock: testkit.Clock{Value: fixedCoreTime()},
		Transfer: &TransferConfig{
			TargetEnvironmentID: "internal",
			PublicKeys:          map[string]ed25519.PublicKey{"key-1": public},
			ReturnSigner:        signer,
			RuntimeBindingResolver: transfer.RuntimeBindingResolverFunc(func(_ context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
				return model.RuntimeBinding{LogicalActorID: logicalID, Revision: 3, EnvironmentID: environmentID, RuntimePrincipalID: "target-" + logicalID, AgentTeamsInstanceID: "TEAM-INTERNAL"}, nil
			}),
			ProvenanceVerifiers: transfer.ProvenanceVerifierSet{"git": transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil })},
			NewEventID: func() string {
				id, _ := ids.New("XFR")
				return id
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if project.Transfer == nil {
		t.Fatal("Open() did not assemble transfer service")
	}
	if _, ok := project.Transfer.Writer.(transfer.CoreTeamWriter); !ok {
		t.Fatalf("transfer writer = %T, want production CoreTeamWriter", project.Transfer.Writer)
	}
	archive, err := transfer.ExportBytes(transfer.ExportInput{Manifest: transferManifestFixture(), Signer: signer, ProvenanceVerifier: transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := project.Transfer.PreviewImport(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Transfer.ApplyImport(ctx, preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}); err != nil {
		t.Fatal(err)
	}
	change := transfer.Entry{Type: transfer.EntryGitDiff, Path: "git/diff/return.patch", Data: []byte(`{"patch":"return"}`), Provenance: transfer.EntryProvenance{Source: "git", SHA256: coreDigest([]byte(`{"patch":"return"}`))}}
	request := transfer.ReturnRequest{Base: transferManifestFixture(), Changes: []transfer.ApprovedChange{{Entry: change}}, ApprovedEntryHashes: []string{transfer.EntryApprovalHash(change)}}
	request.Approval = transfer.ReturnApproval{ID: "APR-RETURN", PayloadSHA256: transfer.ReturnApprovalHash(request)}
	appendCoreReturnApproval(t, root, request.Approval)
	if _, err := project.Transfer.BuildReturn(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func fixedCoreTime() time.Time { return time.Date(2026, time.August, 11, 0, 0, 0, 0, time.UTC) }

func transferManifestFixture() transfer.Manifest {
	return transfer.Manifest{
		ProtocolVersion: transfer.ProtocolVersion, TransferID: "XFR-001", ProjectID: "PRJ-TEST", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", GoalVersion: 1,
		MissionIDs: []string{"MSN-001"}, TraceIDs: []string{"TRC-001"}, ContextID: "CTX-001", ContextHash: "context", LeaseID: "LSE-001", Scope: []string{"src/**"}, Skills: []transfer.SkillRef{{Name: "patch", Version: "1.0.0"}},
		Agents:    []transfer.RuntimeHistory{{LogicalActorID: "AGT-BUILD", Bindings: []model.RuntimeBinding{{LogicalActorID: "AGT-BUILD", Revision: 1, EnvironmentID: "public", RuntimePrincipalID: "source-build", AgentTeamsInstanceID: "TEAM-PUBLIC"}}}},
		CreatedAt: fixedCoreTime(), ExpiresAt: fixedCoreTime().Add(time.Hour),
	}
}

func coreDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func appendCoreReturnApproval(t *testing.T, root string, approval transfer.ReturnApproval) {
	t.Helper()
	requested, err := json.Marshal(model.ApprovalRequested{Approval: model.ApprovalRequest{ID: approval.ID, SubjectType: "transfer", SubjectID: "XFR-001", PayloadSHA256: approval.PayloadSHA256, RiskLevel: "L2", RequesterID: "AGT-BUILD"}})
	if err != nil {
		t.Fatal(err)
	}
	decided, err := json.Marshal(model.ApprovalDecided{ApprovalID: approval.ID, PayloadSHA256: approval.PayloadSHA256, Decision: "approved", DeciderID: "USR-LEAD"})
	if err != nil {
		t.Fatal(err)
	}
	store := eventstore.New(root)
	for _, event := range []model.Event{
		{ID: "EVT-TRANSFER-REQUEST", Type: "approval.requested", AggregateType: "approval", AggregateID: approval.ID, Actor: model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}, OccurredAt: fixedCoreTime(), Payload: requested},
		{ID: "EVT-TRANSFER-DECIDED", Type: "approval.decided", AggregateType: "approval", AggregateID: approval.ID, Actor: model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}, OccurredAt: fixedCoreTime(), Payload: decided},
	} {
		if _, err := store.Append(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalProjectUsesEventStore(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)

	project, err := Open(ctx, root, Dependencies{IDs: &testkit.IDs{}, Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reflect.TypeOf(project.Events).String(), "eventstore.Store"; got != want {
		t.Fatalf("personal Project.Events = %s, want %s", got, want)
	}
}

func TestJoinedProjectUsesTeamRepositoryAndPreservesDomainState(t *testing.T) {
	ctx := context.Background()
	root, manifest := initializeCapsule(t, ctx)
	personalEvents, err := eventstore.New(root).ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".haowork", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".haowork", "team", "events.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	accepted := eventstore.NewAt(filepath.Join(root, ".haowork", "team", "events.jsonl"), filepath.Join(root, ".haowork", "team", "events.lock"))
	if _, err := accepted.ImportAcceptedBatch(ctx, personalEvents); err != nil {
		t.Fatal(err)
	}
	if err := teamsync.SaveConfig(ctx, root, teamsync.ClientConfig{
		Endpoint:      "http://127.0.0.1:8787",
		DeviceID:      "DEV-TEST",
		TeamProjectID: manifest.ProjectID,
	}); err != nil {
		t.Fatal(err)
	}

	project, err := Open(ctx, root, Dependencies{IDs: &testkit.IDs{}, Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reflect.TypeOf(project.Events).String(), "*teamsync.Repository"; got != want {
		t.Fatalf("joined Project.Events = %s, want %s", got, want)
	}
	state, err := project.Service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != manifest.ProjectID || state.Goal.Version != manifest.CurrentGoalVersion {
		t.Fatalf("joined project state = %#v, want project %q goal v%d", state, manifest.ProjectID, manifest.CurrentGoalVersion)
	}
}

func TestOpenJoinedProjectRoutesTransferThroughTeamRepository(t *testing.T) {
	ctx := context.Background()
	root, manifest := initializeCapsule(t, ctx)
	personal := eventstore.New(root)
	personalEvents, err := personal.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".haowork", "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".haowork", "team", "events.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	accepted := eventstore.NewAt(filepath.Join(root, ".haowork", "team", "events.jsonl"), filepath.Join(root, ".haowork", "team", "events.lock"))
	if _, err := accepted.ImportAcceptedBatch(ctx, personalEvents); err != nil {
		t.Fatal(err)
	}
	agentPayload, err := json.Marshal(model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accepted.Append(ctx, model.Event{ID: "EVT-TEAM-AGENT", Type: "agent.identity.registered", ProjectID: manifest.ProjectID, GoalVersion: 1, AggregateType: "agent", AggregateID: "AGT-BUILD", Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}, OccurredAt: fixedCoreTime(), Payload: agentPayload}); err != nil {
		t.Fatal(err)
	}
	if err := teamsync.SaveConfig(ctx, root, teamsync.ClientConfig{Endpoint: "http://127.0.0.1:8787", DeviceID: "DEV-TRANSFER", TeamProjectID: manifest.ProjectID}); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ids := &testkit.IDs{}
	project, err := Open(ctx, root, Dependencies{IDs: ids, Clock: testkit.Clock{Value: fixedCoreTime()}, Transfer: &TransferConfig{
		TargetEnvironmentID: "internal", PublicKeys: map[string]ed25519.PublicKey{"key-1": public}, ReturnSigner: transfer.NewEd25519Signer("key-1", private),
		RuntimeBindingResolver: transfer.RuntimeBindingResolverFunc(func(_ context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
			return model.RuntimeBinding{LogicalActorID: logicalID, Revision: 0, EnvironmentID: environmentID, RuntimePrincipalID: "target-" + logicalID, AgentTeamsInstanceID: "TEAM-INTERNAL"}, nil
		}),
		ProvenanceVerifiers: transfer.ProvenanceVerifierSet{"git": transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil })},
		NewEventID:          func() string { id, _ := ids.New("XFR"); return id },
	}})
	if err != nil {
		t.Fatal(err)
	}
	writer, ok := project.Transfer.Writer.(transfer.CoreTeamWriter)
	if !ok {
		t.Fatalf("transfer writer = %T", project.Transfer.Writer)
	}
	sink, ok := writer.Appender.(repositoryBatchSink)
	if !ok || sink.events != project.Events {
		t.Fatalf("transfer writer does not use joined Team repository: %#v", writer.Appender)
	}
	approvalVerifier, ok := project.Transfer.ApprovalVerifier.(transfer.EventStoreApprovalVerifier)
	if !ok || approvalVerifier.Events != project.Events {
		t.Fatalf("transfer approval verifier does not use joined Team repository: %#v", project.Transfer.ApprovalVerifier)
	}
	if _, err := accepted.ReadAll(ctx); err != nil {
		t.Fatalf("Team accepted ledger %s was not used: %v", filepath.Join(root, ".haowork", "team", "events.jsonl"), err)
	}
	archive, err := transfer.ExportBytes(transfer.ExportInput{Manifest: transferManifestFixture(), Signer: transfer.NewEd25519Signer("key-1", private), ProvenanceVerifier: transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := project.Transfer.PreviewImport(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Transfer.ApplyImport(ctx, preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}); err != nil {
		t.Fatal(err)
	}
	personalAfter, err := personal.ReadAll(ctx)
	if err != nil || len(personalAfter) != 1 {
		t.Fatalf("personal ledger received Team transfer events: %#v, %v", personalAfter, err)
	}
	outbox, err := teamsync.NewOutbox(root, "DEV-TRANSFER").ReadAll(ctx)
	if err != nil || len(outbox) != 1 || len(outbox[0].Batch.Events) != 2 || outbox[0].Batch.Events[0].Type != "capsule.imported" || outbox[0].Batch.Events[1].Type != "agent.runtime.bound" {
		t.Fatalf("Team pending transfer batch = %#v, %v", outbox, err)
	}
}

func TestOpenRejectsCorruptManifest(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	manifestPath := filepath.Join(root, ".haowork", "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte("protocol_version: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Open(ctx, root, Dependencies{
		IDs:   &testkit.IDs{},
		Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
	})
	if err == nil {
		t.Fatal("Open() error = nil, want corrupt manifest rejection")
	}
}

func TestOpenKeepsOperationalClassification(t *testing.T) {
	ctx := context.Background()
	root, _ := initializeCapsule(t, ctx)
	if err := os.Remove(filepath.Join(root, ".haowork", "events.jsonl")); err != nil {
		t.Fatal(err)
	}

	project, err := Open(ctx, root, Dependencies{
		IDs:   &testkit.IDs{},
		Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = project.Service.Status(ctx)
	if !errors.Is(err, app.ErrOperational) {
		t.Fatalf("Project.Service.Status() error = %v, want app.ErrOperational", err)
	}
}

func TestOpenJoinedProjectSeedsMissingTeamAcceptedLog(t *testing.T) {
	ctx := context.Background()
	root, manifest := initializeCapsule(t, ctx)
	config := teamsync.ClientConfig{Endpoint: "http://127.0.0.1:8787", DeviceID: "DEVICE-1", EnvironmentID: "ENV-1", PrincipalID: "USR-1", TeamProjectID: manifest.ProjectID}
	if err := teamsync.SaveConfig(ctx, root, config); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, root, Dependencies{IDs: &testkit.IDs{}, Clock: testkit.Clock{Value: time.Now().UTC()}}); err != nil {
		t.Fatalf("Open() joined project: %v", err)
	}
	path := filepath.Join(root, ".haowork", "team", "events.jsonl")
	if bytes, err := os.ReadFile(path); err != nil || len(bytes) != 0 {
		t.Fatalf("seeded Team accepted log = %q, err=%v", bytes, err)
	}
}

func initializeCapsule(t *testing.T, ctx context.Context) (string, capsule.Manifest) {
	t.Helper()
	root := t.TempDir()
	manifest, err := app.InitializeProject(ctx, app.InitializeProjectInput{
		Root:               root,
		Name:               "demo",
		ProjectID:          "PRJ-TEST",
		Goal:               "Keep work auditable",
		Invariants:         []string{"no silent drift"},
		CompletionCriteria: []string{"verified work completes"},
		Actor: model.Actor{
			ID:   "USR-OWNER",
			Kind: model.ActorHuman,
			Role: model.RoleOwner,
		},
	}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

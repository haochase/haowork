package app

import (
	"context"
	"errors"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

const testSCMCommitOID = "0123456789012345678901234567890123456789"
const testSCMMetadataDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSCMServiceObservesConfirmsAndInvalidatesHistory(t *testing.T) {
	ctx := context.Background()
	service, _, run := prepareGovernedBridgeRunningRun(t)
	if err := service.FinishRun(ctx, run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	evidence, err := service.Verify(ctx, VerifyInput{TaskID: run.TaskID, Kind: "test", URI: "artifacts/scm.log", SHA256: testSCMCommitOID, Outcome: "pass", Actor: reviewer("USR-REVIEWER")})
	if err != nil {
		t.Fatal(err)
	}
	missionID := onlyMissionID(t, service)
	inspector := &stubSCMInspector{
		repository: model.SCMRepository{ID: "SCM-001", ProjectID: "PRJ-TEST", Provider: "local-git", ObjectFormat: "sha1", RegisteredAt: testTime},
		observation: model.CommitObservation{
			RepositoryID: "SCM-001", CommitOID: testSCMCommitOID, TreeOID: testSCMCommitOID,
			AuthorName: "Developer", AuthorEmailSHA256: testSCMMetadataDigest, CommitterName: "Developer", CommitterEmailSHA256: testSCMMetadataDigest,
			AuthoredAt: testTime, CommittedAt: testTime, Message: "implement governed change",
			Changes: []model.SCMFileChange{{Path: "internal/app/service.go", Status: "modified", OldBlobOID: testSCMCommitOID, NewBlobOID: testSCMCommitOID}},
		},
		reachable: true,
	}
	if err := service.ConfigureSCM(inspector, t.TempDir()); err != nil {
		t.Fatal(err)
	}

	registered, err := service.RegisterSCM(ctx, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObserveSCMCommit(ctx, registered.ID, testSCMCommitOID, agent("AGT-BUILD")); err != nil {
		t.Fatal(err)
	}
	binding, err := service.ProposeSCMBinding(ctx, ProposeSCMBindingInput{
		RepositoryID: registered.ID,
		CommitOID:    testSCMCommitOID,
		TaskIDs:      []string{run.TaskID},
		MissionID:    missionID,
		EvidenceIDs:  []string{evidence.ID},
		TraceIDs:     []string{"TRC-001"},
		Actor:        agent("AGT-BUILD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := service.ConfirmSCMBinding(ctx, binding.ID, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != "confirmed" {
		t.Fatalf("confirmed binding = %#v", confirmed)
	}
	state, err := service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[run.TaskID].Status == model.StatusCompleted {
		t.Fatal("SCM confirmation completed the governed task")
	}

	inspector.reachable = false
	report, err := service.VerifySCMHistory(ctx, registered.ID, []string{"refs/heads/main"}, reviewer("USR-REVIEWER"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Superseded != 1 || report.Invalidated != 1 {
		t.Fatalf("history report = %#v", report)
	}
	state, err = service.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SCMCommitStatus[model.SCMCommitKey(registered.ID, testSCMCommitOID)] != "superseded" || state.SCMBindings[binding.ID].Status != "invalidated" {
		t.Fatalf("state did not invalidate unreachable history: %#v %#v", state.SCMCommitStatus, state.SCMBindings)
	}
}

func TestSCMServiceRejectsUnapprovedActorWithoutWriting(t *testing.T) {
	service, repository := newWorkflowService(t)
	if err := service.ConfigureSCM(&stubSCMInspector{repository: model.SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-TEST", Provider: "local-git", ObjectFormat: "sha1", RegisteredAt: testTime,
	}}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)
	if _, err := service.RegisterSCM(context.Background(), agent("AGT-001")); err == nil {
		t.Fatal("RegisterSCM() accepted an Agent actor")
	}
	if len(repository.events) != before {
		t.Fatal("rejected SCM registration changed event history")
	}
}

func TestConfigureSCMRejectsRuntimeRebinding(t *testing.T) {
	service, _ := newWorkflowService(t)
	if err := service.ConfigureSCM(&stubSCMInspector{}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureSCM(&stubSCMInspector{}, t.TempDir()); !errors.Is(err, ErrConflict) {
		t.Fatalf("ConfigureSCM() error = %v, want ErrConflict", err)
	}
}

type stubSCMInspector struct {
	repository  model.SCMRepository
	observation model.CommitObservation
	reachable   bool
}

func (inspector *stubSCMInspector) Register(context.Context, string, string) (model.SCMRepository, error) {
	return inspector.repository, nil
}

func (inspector *stubSCMInspector) ObserveCommit(context.Context, string, model.SCMRepository, string) (model.CommitObservation, error) {
	return inspector.observation, nil
}

func (inspector *stubSCMInspector) IsReachable(context.Context, string, string, []string) (bool, error) {
	return inspector.reachable, nil
}

func onlyMissionID(t *testing.T, service *Service) string {
	t.Helper()
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Missions) != 1 {
		t.Fatalf("missions = %#v", state.Missions)
	}
	for id := range state.Missions {
		return id
	}
	return ""
}

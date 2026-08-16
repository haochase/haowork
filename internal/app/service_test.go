package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/mission"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

var testTime = time.Date(2026, 8, 5, 9, 30, 0, 123, time.FixedZone("CST", 8*60*60))

type staticWorkspaceScanner struct {
	changes []model.FileChange
	err     error
}

func (s staticWorkspaceScanner) Scan(context.Context, string) ([]model.FileChange, error) {
	return append([]model.FileChange(nil), s.changes...), s.err
}

type sequenceWorkspaceScanner struct {
	mu    sync.Mutex
	scans [][]model.FileChange
	calls int
}

func (s *sequenceWorkspaceScanner) Scan(context.Context, string) ([]model.FileChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.scans) {
		return nil, errors.New("unexpected workspace scan")
	}
	changes := append([]model.FileChange(nil), s.scans[s.calls]...)
	s.calls++
	return changes, nil
}

func TestAgentCannotApproveRequirement(t *testing.T) {
	service, repository := newWorkflowService(t)
	requirement, _, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: agent("AGT-PLANNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	err = service.Approve(context.Background(), requirement.ID, agent("AGT-APPROVER"))

	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Approve() error = %v, want ErrApprovalRequired", err)
	}
	if len(repository.events) != before {
		t.Fatalf("event count = %d, want %d", len(repository.events), before)
	}
}

func TestIssueMissionPersistsHashBoundEnvelope(t *testing.T) {
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	contextPayload, err := json.Marshal(model.ContextIssued{Context: model.ContextSlice{ID: "CTX-001", TaskID: tasks[0].ID, GoalVersion: 1, Revision: 1, Summary: "task context", SliceHash: "context-hash"}})
	if err != nil {
		t.Fatal(err)
	}
	leasePayload, err := json.Marshal(model.LeaseIssued{Lease: model.Lease{ID: "LSE-001", TaskID: tasks[0].ID, SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/app"}, AllowedSkills: []string{"build"}, Status: "active"}})
	if err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events,
		model.Event{ID: "EVT-AGT-BUILD", Type: "agent.identity.registered", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "agent", AggregateID: "AGT-BUILD", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild}})},
		model.Event{ID: "EVT-AGT-VERIFY", Type: "agent.identity.registered", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "agent", AggregateID: "AGT-VERIFY", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: "AGT-VERIFY", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionVerify}})},
		model.Event{ID: "EVT-CTX-001", Type: "context.issued", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "context", AggregateID: "CTX-001", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: contextPayload},
		model.Event{ID: "EVT-LSE-001", Type: "lease.issued", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "lease", AggregateID: "LSE-001", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: leasePayload},
	)
	before := len(repository.events)

	envelope, err := service.IssueMission(context.Background(), IssueMissionInput{
		TaskIDs: []string{tasks[0].ID}, CompletionCriteria: []string{"tests pass"}, AllowedScopes: []string{"internal/app"},
		Skills: []mission.SkillGrant{{Name: "build", Version: "1"}}, ContextID: "CTX-001", RiskLevel: "L1", EnvironmentID: "ENV-001", PolicyVersion: "POL-1",
		Assignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"}, Deadline: testTime.Add(time.Hour), Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Hash == "" {
		t.Fatal("IssueMission() hash = empty")
	}
	if got := len(repository.events); got != before+1 {
		t.Fatalf("event count = %d, want %d", got, before+1)
	}
	event := repository.events[len(repository.events)-1]
	if event.Type != "mission.issued" {
		t.Fatalf("event type = %q, want mission.issued", event.Type)
	}
	var payload model.MissionIssued
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Envelope.Hash != envelope.Hash {
		t.Fatalf("persisted hash = %q, want %q", payload.Envelope.Hash, envelope.Hash)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestStartRunRequiresApprovedTask(t *testing.T) {
	service, repository := newWorkflowService(t)
	_, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	_, err = service.StartRun(context.Background(), tasks[0].ID, "codex", agent("AGT-001"))

	if !errors.Is(err, ErrConflict) {
		t.Fatalf("StartRun() error = %v, want ErrConflict", err)
	}
	if len(repository.events) != before {
		t.Fatalf("event count = %d, want %d", len(repository.events), before)
	}
}

func TestFinishRunMovesTaskToVerifyingNotCompleted(t *testing.T) {
	service, _, _, task, run := prepareRunningTask(t)

	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tasks[task.ID].Status; got != model.StatusVerifying {
		t.Fatalf("task status = %q, want %q", got, model.StatusVerifying)
	}
	if got := state.Runs[run.ID].Status; got != model.StatusFinished {
		t.Fatalf("run status = %q, want %q", got, model.StatusFinished)
	}
}

func TestVerifyRejectsUnattributedCurrentChange(t *testing.T) {
	repository := initializedRepository(t)
	service := NewWithWorkspaceScanner(
		"PRJ-TEST",
		1,
		repository,
		&testkit.IDs{},
		testkit.Clock{Value: testTime},
		staticWorkspaceScanner{changes: []model.FileChange{{
			Path: "api.go", Status: "modified", SHA256: "changed", Baseline: "baseline",
		}}},
		t.TempDir(),
	)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	run, err := service.StartRun(context.Background(), tasks[0].ID, "codex", agent("AGT-001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	_, err = service.Verify(context.Background(), VerifyInput{
		TaskID: tasks[0].ID, Kind: "test", URI: "test.log", SHA256: "evidence", Outcome: "pass", Actor: reviewer("USR-REVIEWER"),
	})
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("Verify() error = %v, want ErrGateFailed", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestVerifyAllowsCleanWorkspace(t *testing.T) {
	repository := initializedRepository(t)
	service := NewWithWorkspaceScanner(
		"PRJ-TEST",
		1,
		repository,
		&testkit.IDs{},
		testkit.Clock{Value: testTime},
		staticWorkspaceScanner{},
		t.TempDir(),
	)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	run, err := service.StartRun(context.Background(), tasks[0].ID, "codex", agent("AGT-001"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Verify(context.Background(), VerifyInput{
		TaskID: tasks[0].ID, Kind: "test", URI: "test.log", SHA256: "evidence", Outcome: "pass", Actor: reviewer("USR-REVIEWER"),
	}); err != nil {
		t.Fatalf("Verify() error = %v, want clean workspace to pass", err)
	}
}

func TestVerifyRejectsWorkspaceChangeAfterAttributedScanWithoutAppendingEvidence(t *testing.T) {
	service, repository, _, task, run := prepareRunningTask(t)
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	change := model.FileChange{Path: "api.go", Status: "modified", SHA256: "sha-before", Baseline: "baseline"}
	if err := service.RecordScan(context.Background(), []model.FileChange{change}, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	if err := service.AttributeChange(context.Background(), change.Path, change.SHA256, task.ID, "owned by task", owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	scanner := &sequenceWorkspaceScanner{scans: [][]model.FileChange{
		{change},
		{{Path: "api.go", Status: "modified", SHA256: "sha-after", Baseline: "baseline"}},
	}}
	service.ConfigureWorkspaceScanner(scanner, t.TempDir())
	before := len(repository.events)

	_, err := service.Verify(context.Background(), VerifyInput{
		TaskID: task.ID, Kind: "test", URI: "test.log", SHA256: "evidence", Outcome: "pass", Actor: reviewer("USR-REVIEWER"),
	})
	if !errors.Is(err, ErrOperational) {
		t.Fatalf("Verify() error = %v, want retryable ErrOperational", err)
	}
	if scanner.calls != 2 {
		t.Fatalf("workspace scans = %d, want confirmation scan", scanner.calls)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count after unstable workspace verification = %d, want %d", got, before)
	}
}

func TestAttributeChangeRequiresKnownTaskOrManualNote(t *testing.T) {
	service, repository, _, task, _ := prepareRunningTask(t)
	change := model.FileChange{Path: "api.go", Status: "modified", SHA256: "changed", Baseline: "baseline"}
	if err := service.RecordScan(context.Background(), []model.FileChange{change}, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	err := service.AttributeChange(context.Background(), change.Path, change.SHA256, "TSK-MISSING", "", owner("USR-OWNER"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AttributeChange() error = %v, want ErrConflict for unknown task", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count after unknown task = %d, want %d", got, before)
	}

	err = service.AttributeChange(context.Background(), change.Path, change.SHA256, "external-manual", "", owner("USR-OWNER"))
	if err == nil {
		t.Fatal("AttributeChange() error = nil, want manual attribution note error")
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count after blank manual note = %d, want %d", got, before)
	}

	if err := service.AttributeChange(context.Background(), change.Path, change.SHA256, task.ID, "implemented by this task", owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Changes[change.Path].Attributed {
		t.Fatal("attributed change is not marked in the projection")
	}

	deleted := model.FileChange{Path: "removed.go", Status: "deleted", Baseline: "baseline"}
	if err := service.RecordScan(context.Background(), []model.FileChange{change, deleted}, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	if err := service.AttributeChange(context.Background(), deleted.Path, "", "external-manual", "removed outside a task", owner("USR-OWNER")); err != nil {
		t.Fatalf("AttributeChange() error = %v, want deletion manual attribution to succeed", err)
	}
	state, err = service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Changes[deleted.Path].Attributed {
		t.Fatal("deleted manual change is not marked in the projection")
	}
}

func TestFinishRunRequiresStarterOrHumanOwner(t *testing.T) {
	service, repository, _, _, run := prepareRunningTask(t)
	before := len(repository.events)

	err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-OTHER"))

	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("FinishRun() error = %v, want ErrApprovalRequired", err)
	}
	if len(repository.events) != before {
		t.Fatalf("event count = %d, want %d", len(repository.events), before)
	}
}

func TestCompleteRequiresPassingEvidence(t *testing.T) {
	service, repository, _, task, run := prepareRunningTask(t)
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), VerifyInput{
		TaskID: task.ID, Kind: "test", URI: "test.log", SHA256: "abc", Outcome: "fail", Actor: reviewer("USR-REVIEWER"),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	err := service.Complete(context.Background(), task.ID, reviewer("USR-REVIEWER"))

	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("Complete() error = %v, want ErrGateFailed", err)
	}
	if len(repository.events) != before {
		t.Fatalf("event count = %d, want %d", len(repository.events), before)
	}

	err = service.Complete(context.Background(), task.ID, model.Actor{ID: "AGT-BAD", Kind: model.ActorAgent, Role: model.RoleOwner})
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("Complete() with invalid actor error = %v, want gate checked before authority", err)
	}
}

func TestAgentCannotCompleteOwnTask(t *testing.T) {
	service, repository, _, task, run := prepareRunningTask(t)
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), VerifyInput{
		TaskID: task.ID, Kind: "test", URI: "test.log", SHA256: "abc", Outcome: "pass", Actor: agent("AGT-001"),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)

	err := service.Complete(context.Background(), task.ID, agent("AGT-001"))

	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Complete() error = %v, want ErrApprovalRequired", err)
	}
	if len(repository.events) != before {
		t.Fatalf("event count = %d, want %d", len(repository.events), before)
	}
}

func TestReviewerCanCompleteVerifiedTask(t *testing.T) {
	service, _, _, task, run := prepareRunningTask(t)
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(context.Background(), VerifyInput{
		TaskID: task.ID, Kind: "test", URI: "test.log", SHA256: "abc", Outcome: "pass", Actor: reviewer("USR-REVIEWER"),
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.Complete(context.Background(), task.ID, reviewer("USR-REVIEWER")); err != nil {
		t.Fatal(err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tasks[task.ID].Status; got != model.StatusCompleted {
		t.Fatalf("task status = %q, want %q", got, model.StatusCompleted)
	}
}

func TestHistoryFiltersByAggregateID(t *testing.T) {
	service, _, requirement, task, _ := prepareRunningTask(t)

	events, err := service.History(context.Background(), requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("requirement history count = %d, want 2", len(events))
	}
	for _, event := range events {
		if event.AggregateID != requirement.ID {
			t.Fatalf("AggregateID = %q, want %q", event.AggregateID, requirement.ID)
		}
	}
	all, err := service.History(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= len(events) || all[0].AggregateID == task.ID {
		t.Fatalf("unfiltered history = %#v, want all events", all)
	}
}

func TestInitializeProjectCreatesManifestAndFirstEvent(t *testing.T) {
	root := t.TempDir()
	ids := &testkit.IDs{}
	manifest, err := InitializeProject(context.Background(), InitializeProjectInput{
		Root: root, Name: "demo", Goal: "Keep work auditable",
		Invariants: []string{"no silent drift"}, CompletionCriteria: []string{"verified work completes"}, Actor: owner("USR-OWNER"),
	}, ids, testkit.Clock{Value: testTime})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProjectID != "PRJ-001" || manifest.CurrentGoalVersion != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	events, err := eventstore.New(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "project.initialized" || events[0].ID != "EVT-002" {
		t.Fatalf("events = %#v, want one project.initialized EVT-002", events)
	}
	if events[0].OccurredAt.Location() != time.UTC {
		t.Fatalf("OccurredAt location = %v, want UTC", events[0].OccurredAt.Location())
	}
	var payload model.ProjectInitialized
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantGoal := model.GoalVersion{
		Version: 1, Statement: "Keep work auditable", Invariants: []string{"no silent drift"}, CompletionCriteria: []string{"verified work completes"},
	}
	if payload.Name != "demo" || !goalsEqual(payload.Goal, wantGoal) {
		t.Fatalf("payload = %#v, want name and complete goal %#v", payload, wantGoal)
	}
}

func TestInitializeProjectRequiresGoalAndCompletionCriteria(t *testing.T) {
	tests := []struct {
		name       string
		goal       string
		criteria   []string
		invariants []string
	}{
		{name: "empty goal", criteria: []string{"done"}},
		{name: "blank goal", goal: "  ", criteria: []string{"done"}},
		{name: "missing criteria", goal: "goal"},
		{name: "blank criteria", goal: "goal", criteria: []string{"  "}},
		{name: "blank invariant", goal: "goal", criteria: []string{"done"}, invariants: []string{"  "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := InitializeProject(context.Background(), InitializeProjectInput{
				Root: root, Name: "demo", Goal: test.goal, Invariants: test.invariants,
				CompletionCriteria: test.criteria, Actor: owner("USR-OWNER"),
			}, &testkit.IDs{}, testkit.Clock{Value: testTime})
			if err == nil {
				t.Fatal("InitializeProject() succeeded, want validation error")
			}
			if errors.Is(err, ErrOperational) {
				t.Fatalf("InitializeProject() validation error = %v, must not be operational", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, ".haowork")); !os.IsNotExist(statErr) {
				t.Fatalf("capsule exists after validation failure: %v", statErr)
			}
		})
	}
}

func TestValidationErrorsAreNotOperational(t *testing.T) {
	service, _ := newWorkflowService(t)

	_, _, err := service.Plan(context.Background(), PlanInput{Actor: owner("USR-OWNER")})

	if err == nil || errors.Is(err, ErrOperational) {
		t.Fatalf("Plan() validation error = %v, want non-operational error", err)
	}
}

func TestInitializeProjectCanceledContextIsOperational(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := InitializeProject(ctx, InitializeProjectInput{
		Root: "", Name: "demo", Goal: "goal", CompletionCriteria: []string{"done"}, Actor: owner("USR-OWNER"),
	}, &testkit.IDs{}, testkit.Clock{Value: testTime})

	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOperational) {
		t.Fatalf("InitializeProject() error = %v, want context.Canceled and ErrOperational", err)
	}
}

func TestInitializeProjectCapsuleInitFailureIsOperational(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-parent", "project")

	_, err := InitializeProject(context.Background(), InitializeProjectInput{
		Root: root, Name: "demo", Goal: "goal", CompletionCriteria: []string{"done"}, Actor: owner("USR-OWNER"),
	}, &testkit.IDs{}, testkit.Clock{Value: testTime})

	if !errors.Is(err, ErrOperational) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("InitializeProject() error = %v, want original filesystem error and ErrOperational", err)
	}
}

func TestInitializeProjectRollsBackNewCapsuleWhenFirstAppendFails(t *testing.T) {
	root := t.TempDir()
	appendErr := errors.New("injected append failure")
	repository := &memoryRepository{appendErr: appendErr}
	_, err := initializeProject(context.Background(), InitializeProjectInput{
		Root: root, Name: "demo", Goal: "goal", CompletionCriteria: []string{"done"}, Actor: owner("USR-OWNER"),
	}, &testkit.IDs{}, testkit.Clock{Value: testTime}, func(string) EventRepository { return repository })

	if !errors.Is(err, appendErr) {
		t.Fatalf("initializeProject() error = %v, want append error", err)
	}
	if !errors.Is(err, ErrOperational) {
		t.Fatalf("initializeProject() error = %v, want ErrOperational", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".haowork")); !os.IsNotExist(statErr) {
		t.Fatalf("capsule exists after rollback: %v", statErr)
	}
}

func TestInitializeProjectNeverRemovesPreexistingCapsule(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, ".haowork", "keep.txt")
	if err := os.Mkdir(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	repository := &memoryRepository{appendErr: errors.New("must not append")}
	_, err := initializeProject(context.Background(), InitializeProjectInput{
		Root: root, Name: "demo", Goal: "goal", CompletionCriteria: []string{"done"}, Actor: owner("USR-OWNER"),
	}, &testkit.IDs{}, testkit.Clock{Value: testTime}, func(string) EventRepository { return repository })

	if err == nil {
		t.Fatal("initializeProject() succeeded, want existing capsule error")
	}
	if errors.Is(err, ErrOperational) || !errors.Is(err, capsule.ErrAlreadyExists) {
		t.Fatalf("initializeProject() error = %v, want non-operational existing-capsule error", err)
	}
	data, readErr := os.ReadFile(marker)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("preexisting marker = %q, %v; want preserved", data, readErr)
	}
	if repository.appendCalls != 0 {
		t.Fatalf("append calls = %d, want 0", repository.appendCalls)
	}
}

func TestIDGenerationFailureDoesNotAppendEvent(t *testing.T) {
	t.Run("workflow event", func(t *testing.T) {
		repository := initializedRepository(t)
		ids := &failingIDs{failAt: 2}
		service := New("PRJ-TEST", 1, repository, ids, testkit.Clock{Value: testTime})
		before := len(repository.events)

		_, _, err := service.Plan(context.Background(), PlanInput{
			Title: "Govern changes", Tasks: []TaskInput{{Title: "Implement", AcceptanceCriteria: []string{"done"}}}, Actor: owner("USR-OWNER"),
		})

		if !errors.Is(err, errIDGeneration) {
			t.Fatalf("Plan() error = %v, want ID generation error", err)
		}
		if !errors.Is(err, ErrOperational) {
			t.Fatalf("Plan() error = %v, want ErrOperational", err)
		}
		if len(repository.events) != before || repository.appendCalls != 0 {
			t.Fatalf("repository changed: events=%d appendCalls=%d", len(repository.events), repository.appendCalls)
		}
	})

	t.Run("initial event", func(t *testing.T) {
		root := t.TempDir()
		repository := &memoryRepository{}
		_, err := initializeProject(context.Background(), InitializeProjectInput{
			Root: root, Name: "demo", Goal: "goal", CompletionCriteria: []string{"done"}, Actor: owner("USR-OWNER"),
		}, &failingIDs{failAt: 2}, testkit.Clock{Value: testTime}, func(string) EventRepository { return repository })
		if !errors.Is(err, errIDGeneration) {
			t.Fatalf("initializeProject() error = %v, want ID generation error", err)
		}
		if !errors.Is(err, ErrOperational) {
			t.Fatalf("initializeProject() error = %v, want ErrOperational", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, ".haowork")); !os.IsNotExist(statErr) {
			t.Fatalf("capsule exists after ID failure: %v", statErr)
		}
		if repository.appendCalls != 0 {
			t.Fatalf("append calls = %d, want 0", repository.appendCalls)
		}
	})
}

func TestClockFailureDoesNotAppendEvent(t *testing.T) {
	repository := initializedRepository(t)
	service := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{})
	before := len(repository.events)

	_, _, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes", Tasks: []TaskInput{{Title: "Implement", AcceptanceCriteria: []string{"done"}}}, Actor: owner("USR-OWNER"),
	})

	if err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("Plan() error = %v, want clock error", err)
	}
	if !errors.Is(err, ErrOperational) {
		t.Fatalf("Plan() error = %v, want ErrOperational", err)
	}
	if len(repository.events) != before || repository.appendCalls != 0 {
		t.Fatalf("repository changed: events=%d appendCalls=%d", len(repository.events), repository.appendCalls)
	}
}

func TestStatusUsesReplayedGoalVersionAfterApproval(t *testing.T) {
	repository := replayedGoalVersionRepository(t)
	service := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})

	state, err := service.Status(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if got := state.Goal.Version; got != 2 {
		t.Fatalf("status goal version = %d, want replayed version 2", got)
	}
}

func TestServiceUsesReplayedGoalVersionAfterApproval(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("approved goal context"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := replayedGoalVersionRepository(t)
	service := NewWithWorkspaceScanner("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, staticWorkspaceScanner{}, root)

	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Plan against the approved goal",
		Tasks: []TaskInput{{Title: "Build current context", AcceptanceCriteria: []string{"context is current"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	planned := repository.events[len(repository.events)-1]
	if planned.Type != "requirement.planned" || planned.GoalVersion != 2 {
		t.Fatalf("planned event = %#v, want goal version 2", planned)
	}
	var plannedPayload model.RequirementPlanned
	if err := json.Unmarshal(planned.Payload, &plannedPayload); err != nil {
		t.Fatal(err)
	}
	if plannedPayload.Requirement.GoalVersion != 2 || plannedPayload.Tasks[0].GoalVersion != 2 {
		t.Fatalf("planned payload = %#v, want goal version 2", plannedPayload)
	}

	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	contextSlice, err := service.BuildContext(context.Background(), ContextBuildInput{
		TaskID:  tasks[0].ID,
		Sources: []string{"brief.txt"},
		Reason:  "prepare execution",
		Actor:   owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	issued := repository.events[len(repository.events)-1]
	if issued.Type != "context.issued" || issued.GoalVersion != 2 || contextSlice.GoalVersion != 2 {
		t.Fatalf("context issue = event %#v slice %#v, want goal version 2", issued, contextSlice)
	}
	var issuedPayload model.ContextIssued
	if err := json.Unmarshal(issued.Payload, &issuedPayload); err != nil {
		t.Fatal(err)
	}
	if issuedPayload.Context.GoalVersion != 2 {
		t.Fatalf("issued payload = %#v, want goal version 2", issuedPayload)
	}
}

func TestReadAllFailureIsOperational(t *testing.T) {
	repository := initializedRepository(t)
	readErr := errors.New("injected read failure")
	repository.readErr = readErr
	service := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})

	_, err := service.Status(context.Background())

	if !errors.Is(err, readErr) || !errors.Is(err, ErrOperational) {
		t.Fatalf("Status() error = %v, want original read error and ErrOperational", err)
	}
}

func TestAppendFailureIsOperational(t *testing.T) {
	repository := initializedRepository(t)
	appendErr := errors.New("injected append failure")
	repository.appendErr = appendErr
	service := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})

	_, _, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes", Tasks: []TaskInput{{Title: "Implement", AcceptanceCriteria: []string{"done"}}}, Actor: owner("USR-OWNER"),
	})

	if !errors.Is(err, appendErr) || !errors.Is(err, ErrOperational) {
		t.Fatalf("Plan() error = %v, want original append error and ErrOperational", err)
	}
}

func TestConcurrentApprovalUsesAtomicSnapshot(t *testing.T) {
	base := initializedRepository(t)
	planner := New("PRJ-TEST", 1, base, &testkit.IDs{}, testkit.Clock{Value: testTime})
	requirement, _, err := planner.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := newApprovalBarrierRepository(base, 12)
	ids := &atomicIDs{}
	errorsCh := make(chan error, 12)
	var group sync.WaitGroup
	for index := 0; index < 12; index++ {
		service := New("PRJ-TEST", 1, repository, ids, testkit.Clock{Value: testTime})
		group.Add(1)
		go func() {
			defer group.Done()
			errorsCh <- service.Approve(context.Background(), requirement.ID, owner("USR-OWNER"))
		}()
	}
	group.Wait()
	close(errorsCh)

	successes := 0
	for err := range errorsCh {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("concurrent Approve() error = %v, want conflict or one success", err)
		}
		if errors.Is(err, ErrOperational) {
			t.Fatalf("concurrent Approve() error = %v, want conflict without ErrOperational", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful approvals = %d, want exactly 1", successes)
	}
	state, err := planner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() after concurrent approvals = %v, want valid replay", err)
	}
	if got := state.Requirements[requirement.ID].Status; got != model.StatusApproved {
		t.Fatalf("requirement status = %q, want %q", got, model.StatusApproved)
	}
	events, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want initialization, plan, and one approval", len(events))
	}
}

type memoryRepository struct {
	mu          sync.Mutex
	events      []model.Event
	appendErr   error
	readErr     error
	appendCalls int
}

func (r *memoryRepository) Append(_ context.Context, event model.Event) (model.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendCalls++
	if r.appendErr != nil {
		return model.Event{}, r.appendErr
	}
	event.Sequence = uint64(len(r.events) + 1)
	r.events = append(r.events, event)
	return event, nil
}

func (r *memoryRepository) ReadAll(context.Context) ([]model.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readErr != nil {
		return nil, r.readErr
	}
	return append([]model.Event(nil), r.events...), nil
}

func (r *memoryRepository) AppendIfUnchanged(ctx context.Context, event model.Event, expectedEventCount int) (model.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != expectedEventCount {
		return model.Event{}, eventstore.ErrStateChanged
	}
	return r.appendLocked(ctx, event)
}

func (r *memoryRepository) AppendBatchIfUnchanged(ctx context.Context, events []model.Event, expectedEventCount int) ([]model.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != expectedEventCount {
		return nil, eventstore.ErrStateChanged
	}
	if r.appendErr != nil {
		return nil, r.appendErr
	}
	prepared := make([]model.Event, len(events))
	for index, event := range events {
		event.Sequence = uint64(len(r.events) + index + 1)
		prepared[index] = event
	}
	r.appendCalls += len(prepared)
	r.events = append(r.events, prepared...)
	return prepared, nil
}

func (r *memoryRepository) appendLocked(_ context.Context, event model.Event) (model.Event, error) {
	r.appendCalls++
	if r.appendErr != nil {
		return model.Event{}, r.appendErr
	}
	event.Sequence = uint64(len(r.events) + 1)
	r.events = append(r.events, event)
	return event, nil
}

type approvalBarrierRepository struct {
	delegate    *memoryRepository
	waitFor     int32
	reads       atomic.Int32
	release     chan struct{}
	releaseOnce sync.Once
}

func newApprovalBarrierRepository(delegate *memoryRepository, waitFor int) *approvalBarrierRepository {
	return &approvalBarrierRepository{delegate: delegate, waitFor: int32(waitFor), release: make(chan struct{})}
}

func (r *approvalBarrierRepository) ReadAll(ctx context.Context) ([]model.Event, error) {
	events, err := r.delegate.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	if r.reads.Add(1) == r.waitFor {
		r.releaseOnce.Do(func() { close(r.release) })
	}
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.release:
	case <-timer.C:
		r.releaseOnce.Do(func() { close(r.release) })
	}
	return events, nil
}

func (r *approvalBarrierRepository) Append(ctx context.Context, event model.Event) (model.Event, error) {
	return r.delegate.Append(ctx, event)
}

func (r *approvalBarrierRepository) AppendIfUnchanged(ctx context.Context, event model.Event, expectedEventCount int) (model.Event, error) {
	return r.delegate.AppendIfUnchanged(ctx, event, expectedEventCount)
}

func newWorkflowService(t *testing.T) (*Service, *memoryRepository) {
	t.Helper()
	repository := initializedRepository(t)
	return New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}), repository
}

func initializedRepository(t *testing.T) *memoryRepository {
	t.Helper()
	payload, err := json.Marshal(model.ProjectInitialized{
		Name: "demo",
		Goal: model.GoalVersion{Version: 1, Statement: "Keep work auditable", CompletionCriteria: []string{"verified work completes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &memoryRepository{events: []model.Event{{
		ID: "EVT-INIT", Type: "project.initialized", ProjectID: "PRJ-TEST", GoalVersion: 1,
		AggregateType: "project", AggregateID: "PRJ-TEST", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: payload,
	}}}
}

func replayedGoalVersionRepository(t *testing.T) *memoryRepository {
	t.Helper()
	repository := initializedRepository(t)
	change := model.GoalChange{
		ID:          "GCH-001",
		Reason:      "Approve team synchronization scope",
		Status:      "proposed",
		ProposerID:  "USR-OWNER",
		BaseVersion: 1,
		Proposed: model.GoalVersion{
			Version:            2,
			Statement:          "Coordinate team work",
			Invariants:         []string{"goal changes are approved"},
			CompletionCriteria: []string{"new work uses the approved version"},
		},
		CreatedAt: testTime.UTC(),
	}
	proposalPayload, err := json.Marshal(model.GoalChangeProposed{GoalChange: change})
	if err != nil {
		t.Fatal(err)
	}
	approvalPayload, err := json.Marshal(model.GoalChangeApproved{
		GoalChangeID: change.ID,
		DeciderID:    "USR-OWNER",
		DecidedAt:    testTime.UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events,
		model.Event{
			ID: "EVT-GCH-001", Type: "goal.change.proposed", ProjectID: "PRJ-TEST", GoalVersion: 1,
			AggregateType: "goal_change", AggregateID: change.ID, Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: proposalPayload,
		},
		model.Event{
			ID: "EVT-GCH-002", Type: "goal.change.approved", ProjectID: "PRJ-TEST", GoalVersion: 1,
			AggregateType: "goal_change", AggregateID: change.ID, Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC().Add(time.Minute), Payload: approvalPayload,
		},
	)
	return repository
}

func prepareRunningTask(t *testing.T) (*Service, *memoryRepository, model.Requirement, model.Task, model.Run) {
	t.Helper()
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes",
		Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}},
		Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	run, err := service.StartRun(context.Background(), tasks[0].ID, "codex", agent("AGT-001"))
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, requirement, tasks[0], run
}

func owner(id string) model.Actor {
	return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleOwner}
}

func reviewer(id string) model.Actor {
	return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleReviewer}
}

func agent(id string) model.Actor {
	return model.Actor{ID: id, Kind: model.ActorAgent, Role: model.RoleAgent}
}

func goalsEqual(left, right model.GoalVersion) bool {
	return left.Version == right.Version && left.Statement == right.Statement &&
		strings.Join(left.Invariants, "\x00") == strings.Join(right.Invariants, "\x00") &&
		strings.Join(left.CompletionCriteria, "\x00") == strings.Join(right.CompletionCriteria, "\x00")
}

var errIDGeneration = errors.New("injected id generation failure")

type failingIDs struct {
	calls  int
	failAt int
}

type atomicIDs struct{ next atomic.Int64 }

func (g *atomicIDs) New(prefix string) (string, error) {
	return fmt.Sprintf("%s-CONCURRENT-%03d", prefix, g.next.Add(1)), nil
}

func (g *failingIDs) New(prefix string) (string, error) {
	g.calls++
	if g.calls == g.failAt {
		return "", errIDGeneration
	}
	return prefix + "-OK", nil
}

var _ EventRepository = (*memoryRepository)(nil)

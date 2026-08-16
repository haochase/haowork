package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestBuildContextWritesIssuedEventAndReturnsImmutableRevision(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("first brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := initializedRepository(t)
	service := NewWithWorkspaceScanner("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, staticWorkspaceScanner{}, root)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Build context", Tasks: []TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Reason: "draft", Actor: owner("USR-OWNER")}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("BuildContext() before approval error = %v, want ErrApprovalRequired", err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	first, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Reason: "draft", Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("revised brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, SupersedesID: first.ID, Sources: []string{"brief.txt"}, Reason: "revised", Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision+1 || second.SupersedesID != first.ID {
		t.Fatalf("second context = %#v, want revision after %q", second, first.ID)
	}
	persistedFirst, err := service.GetContext(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persistedFirst.Superseded || persistedFirst.SliceHash != first.SliceHash || persistedFirst.Sources[0].Digest != first.Sources[0].Digest {
		t.Fatalf("persisted first context = %#v, want immutable original data marked superseded", persistedFirst)
	}
	if got := repository.events[len(repository.events)-2].Type; got != "context.issued" {
		t.Fatalf("issued event type = %q, want context.issued", got)
	}
	if got := repository.events[len(repository.events)-1].Type; got != "context.superseded" {
		t.Fatalf("superseded event type = %q, want context.superseded", got)
	}
	if _, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, SupersedesID: first.ID, Sources: []string{"brief.txt"}, Reason: "duplicate parent", Actor: owner("USR-OWNER")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("BuildContext() duplicate parent error = %v, want ErrConflict", err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Contexts) != 2 || state.Contexts[second.ID].Revision != 2 {
		t.Fatalf("context lineage = %#v, want one monotonic child", state.Contexts)
	}
}

func TestBuildContextSupersedeBatchFailureLeavesNoIssuedRevision(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &failingSupersedeBatchRepository{memoryRepository: initializedRepository(t)}
	service := NewWithWorkspaceScanner("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, staticWorkspaceScanner{}, root)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{Title: "Build context", Tasks: []TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	first, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	repository.failBatch = true
	if _, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, SupersedesID: first.ID, Sources: []string{"brief.txt"}, Actor: owner("USR-OWNER")}); err == nil {
		t.Fatal("BuildContext() succeeded despite injected batch failure")
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Contexts) != 1 || state.Contexts[first.ID].Superseded {
		t.Fatalf("contexts after failed supersede = %#v, want only active parent", state.Contexts)
	}
}

type failingSupersedeBatchRepository struct {
	*memoryRepository
	failBatch bool
}

func (r *failingSupersedeBatchRepository) AppendIfUnchanged(ctx context.Context, event model.Event, expectedEventCount int) (model.Event, error) {
	if event.Type == "context.superseded" && r.failBatch {
		return model.Event{}, errors.New("injected second event failure")
	}
	return r.memoryRepository.AppendIfUnchanged(ctx, event, expectedEventCount)
}

func (r *failingSupersedeBatchRepository) AppendBatchIfUnchanged(context.Context, []model.Event, int) ([]model.Event, error) {
	if r.failBatch {
		return nil, errors.New("injected batch failure")
	}
	return nil, errors.New("unexpected batch call")
}

func TestBuildContextRejectsDraftTask(t *testing.T) {
	service, _ := newWorkflowService(t)
	_, tasks, err := service.Plan(context.Background(), PlanInput{Title: "Build context", Tasks: []TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"missing.txt"}, Actor: owner("USR-OWNER")}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("draft context error = %v, want ErrApprovalRequired", err)
	}
}

func TestBuildContextUsesReplayedGoalVersionAndRejectsActualStaleTask(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := initializedRepository(t)
	current := NewWithWorkspaceScanner("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, staticWorkspaceScanner{}, root)
	requirement, tasks, err := current.Plan(context.Background(), PlanInput{Title: "Build context", Tasks: []TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	replayed := NewWithWorkspaceScanner("PRJ-TEST", 2, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, staticWorkspaceScanner{}, root)
	contextSlice, err := replayed.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Actor: owner("USR-OWNER")})
	if err != nil {
		t.Fatal(err)
	}
	if contextSlice.GoalVersion != 1 {
		t.Fatalf("context goal version = %d, want replayed version 1", contextSlice.GoalVersion)
	}
	if got := repository.events[len(repository.events)-1].GoalVersion; got != 1 {
		t.Fatalf("context event goal version = %d, want replayed version 1", got)
	}

	change := model.GoalChange{
		ID:          "GCH-CONTEXT-001",
		Reason:      "Advance the goal",
		Status:      "proposed",
		ProposerID:  "USR-OWNER",
		BaseVersion: 1,
		Proposed: model.GoalVersion{
			Version:            2,
			Statement:          "Coordinate current work",
			CompletionCriteria: []string{"new work uses version 2"},
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
			ID: "EVT-GCH-CONTEXT-001", Type: "goal.change.proposed", ProjectID: "PRJ-TEST", GoalVersion: 1,
			AggregateType: "goal_change", AggregateID: change.ID, Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: proposalPayload,
		},
		model.Event{
			ID: "EVT-GCH-CONTEXT-002", Type: "goal.change.approved", ProjectID: "PRJ-TEST", GoalVersion: 1,
			AggregateType: "goal_change", AggregateID: change.ID, Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC().Add(time.Minute), Payload: approvalPayload,
		},
	)

	if _, err := replayed.BuildContext(context.Background(), ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Actor: owner("USR-OWNER")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("BuildContext() actual stale task error = %v, want ErrConflict", err)
	}
	state, err := replayed.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Contexts[contextSlice.ID].Superseded {
		t.Fatal("approved goal did not supersede the old context")
	}
}

package teamsync

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/testkit"
)

func TestRepositoryReplaysAcceptedHistoryAndPendingOverlay(t *testing.T) {
	root := t.TempDir()
	accepted := eventstore.New(root)
	clock := testkit.Clock{Value: time.Now().UTC()}
	ids := &testkit.IDs{}
	owner := model.Actor{ID: "USR-1", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{Root: root, Name: "test", ProjectID: "PRJ-1", Goal: "deliver", CompletionCriteria: []string{"pass"}, Actor: owner}, ids, clock); err != nil {
		t.Fatal(err)
	}
	project := mustEvents(t, accepted)[0]
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}, clock)
	candidate := plannedEvent(t, project)
	if _, err := repository.AppendIfUnchanged(context.Background(), candidate, 1); err != nil {
		t.Fatalf("AppendIfUnchanged() error = %v", err)
	}
	events, err := repository.ReadAll(context.Background())
	if err != nil || len(events) != 2 || events[1].Sync == nil || events[1].Sync.BatchID == "" {
		t.Fatalf("ReadAll() = %#v, %v", events, err)
	}
	if canonical, err := accepted.ReadAll(context.Background()); err != nil || len(canonical) != 1 {
		t.Fatalf("accepted history changed: %#v, %v", canonical, err)
	}
}

func TestRepositoryAdaptsRealPlanAndStartRunWithoutWritingCanonicalHistory(t *testing.T) {
	root := t.TempDir()
	accepted := eventstore.New(root)
	clock := testkit.Clock{Value: time.Now().UTC()}
	ids := &testkit.IDs{}
	owner := model.Actor{ID: "USR-1", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{Root: root, Name: "test", ProjectID: "PRJ-1", Goal: "deliver", CompletionCriteria: []string{"pass"}, Actor: owner}, ids, clock); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: "PRJ-1"}, clock)
	service := app.New("PRJ-1", 1, repository, ids, clock)
	_, tasks, err := service.Plan(context.Background(), app.PlanInput{Title: "Requirement", Tasks: []app.TaskInput{{Title: "Task", AcceptanceCriteria: []string{"pass"}}}, Actor: owner})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if err := service.Approve(context.Background(), tasks[0].RequirementID, owner); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if _, err := service.StartRun(context.Background(), tasks[0].ID, "executor", owner); err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if canonical := mustEvents(t, accepted); len(canonical) != 1 {
		t.Fatalf("canonical events = %d, want 1", len(canonical))
	}
	entries, err := repository.outbox.ReadAll(context.Background())
	if err != nil || len(entries) != 3 {
		t.Fatalf("outbox entries = %#v, %v", entries, err)
	}
	for _, entry := range entries {
		if entry.Status != Pending || len(entry.Batch.Events) != 1 || entry.Batch.Events[0].Sync == nil || entry.Batch.Events[0].Sync.PayloadSHA256 == "" {
			t.Fatalf("invalid pending outbox entry %#v", entry)
		}
		sync := entry.Batch.Events[0].Sync
		if sync.DeviceID != "DEV-1" || sync.AuthenticatedPrincipal != owner.ID || sync.BaseTeamSeq == 0 || sync.TraceID == "" || sync.TraceID != sync.BatchID || sync.TaskID != tasks[0].ID {
			t.Fatalf("incomplete sync metadata %#v", sync)
		}
	}
}

func TestRepositoryBatchSharesBatchIDAndExcludesRejectedOverlay(t *testing.T) {
	root, accepted, project, clock := initializedRepositoryFixture(t)
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}, clock)
	plan := plannedEvent(t, project)
	approvedPayload, err := json.Marshal(model.RequirementApproved{RequirementID: "REQ-1"})
	if err != nil {
		t.Fatal(err)
	}
	approved := model.Event{ID: "EVT-APPROVE", Type: "requirement.approved", ProjectID: project.ProjectID, GoalVersion: 1, AggregateType: "requirement", AggregateID: "REQ-1", Actor: project.Actor, OccurredAt: project.OccurredAt, Payload: approvedPayload}
	stored, err := repository.AppendBatchIfUnchanged(context.Background(), []model.Event{plan, approved}, 1)
	if err != nil {
		t.Fatalf("AppendBatchIfUnchanged() error = %v", err)
	}
	if stored[0].Sync.BatchID == "" || stored[0].Sync.BatchID != stored[1].Sync.BatchID {
		t.Fatalf("batch ids = %q, %q", stored[0].Sync.BatchID, stored[1].Sync.BatchID)
	}
	if stored[0].Sync.TaskID != "TSK-1" || stored[1].Sync.TaskID != "TSK-1" {
		t.Fatalf("batch task metadata = %q, %q, want TSK-1", stored[0].Sync.TaskID, stored[1].Sync.TaskID)
	}
	if err := repository.outbox.Update(context.Background(), stored[0].Sync.BatchID, team.PushResult{Status: team.PushRejected}); err != nil {
		t.Fatal(err)
	}
	if err := repository.outbox.Append(context.Background(), team.PushBatch{BatchID: "BATCH-CONFLICT", Events: []model.Event{{ID: "EVT-CONFLICT"}}}); err != nil {
		t.Fatal(err)
	}
	if err := repository.outbox.Update(context.Background(), "BATCH-CONFLICT", team.PushResult{Status: team.PushConflict}); err != nil {
		t.Fatal(err)
	}
	events, err := repository.ReadAll(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("rejected/conflict projection = %#v, %v", events, err)
	}
}

func TestRepositoryRejectsForbiddenOfflineEventBeforeOutboxAppend(t *testing.T) {
	root, accepted, project, clock := initializedRepositoryFixture(t)
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}, clock)
	candidate := model.Event{ID: "EVT-FORBIDDEN", Type: "task.completed", ProjectID: project.ProjectID, GoalVersion: 1, Actor: project.Actor, Payload: []byte(`{"task_id":"TSK-1"}`)}
	if _, err := repository.AppendIfUnchanged(context.Background(), candidate, 1); err == nil {
		t.Fatal("AppendIfUnchanged() accepted a forbidden offline event")
	}
	entries, err := repository.outbox.ReadAll(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("forbidden candidate appended: %#v, %v", entries, err)
	}
}

func TestRepositoryCASAcrossInstancesAllowsOnlyOneCandidate(t *testing.T) {
	root, accepted, project, clock := initializedRepositoryFixture(t)
	config := ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}
	left, right := NewRepository(root, accepted, config, clock), NewRepository(root, accepted, config, clock)
	candidates := []model.Event{plannedEvent(t, project), plannedEvent(t, project)}
	candidates[1].ID, candidates[1].AggregateID = "EVT-PLAN-2", "REQ-2"
	candidates[1].Payload = []byte(`{"requirement":{"id":"REQ-2","goal_version":1,"title":"Plan 2","status":"Draft"},"tasks":[{"id":"TSK-2","requirement_id":"REQ-2","goal_version":1,"title":"Task 2","acceptance_criteria":["pass"],"status":"Draft"}]}`)
	var group sync.WaitGroup
	results := make(chan error, 2)
	for index, repository := range []*Repository{left, right} {
		group.Add(1)
		go func(index int, repository *Repository) {
			defer group.Done()
			_, err := repository.AppendIfUnchanged(context.Background(), candidates[index], 1)
			results <- err
		}(index, repository)
	}
	group.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful competing appends = %d, want 1", successes)
	}
	events, err := left.ReadAll(context.Background())
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %#v, %v", events, err)
	}
}

func TestRepositoryRejectsSensitivePayloadBeforeOutboxAppend(t *testing.T) {
	root, accepted, project, clock := initializedRepositoryFixture(t)
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}, clock)
	candidate := plannedEvent(t, project)
	candidate.Payload = []byte(`{"requirement":{"id":"REQ-1","goal_version":1,"title":"Plan","status":"Draft","token":"secret"},"tasks":[{"id":"TSK-1","requirement_id":"REQ-1","goal_version":1,"title":"Task","acceptance_criteria":["pass"],"status":"Draft"}]}`)
	if _, err := repository.AppendIfUnchanged(context.Background(), candidate, 1); err == nil {
		t.Fatal("AppendIfUnchanged() accepted a sensitive payload")
	}
	entries, err := repository.outbox.ReadAll(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("sensitive candidate appended: %#v, %v", entries, err)
	}
}

func TestRepositoryMarksExpiredAcceptedLeaseUnconfirmed(t *testing.T) {
	root, accepted, project, clock := initializedRepositoryFixture(t)
	lease := model.Lease{ID: "LEASE-1", TaskID: "TSK-1", SubjectID: project.Actor.ID, GoalVersion: 1, Revision: 1, StartsAt: clock.Value.Add(-2 * time.Hour), ExpiresAt: clock.Value.Add(-time.Hour), AllowedScopes: []string{"internal/teamsync"}}
	payload, err := json.Marshal(model.LeaseIssued{Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accepted.Append(context.Background(), model.Event{ID: "EVT-LEASE", Type: "lease.issued", ProjectID: project.ProjectID, GoalVersion: 1, AggregateType: "lease", AggregateID: lease.ID, Actor: project.Actor, OccurredAt: project.OccurredAt, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	repository := NewRepository(root, accepted, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project.ProjectID}, clock)
	renewal, err := json.Marshal(model.LeaseRenewed{LeaseID: lease.ID, ExpiresAt: clock.Value.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.AppendIfUnchanged(context.Background(), model.Event{ID: "EVT-RENEW", Type: "lease.renewed", ProjectID: project.ProjectID, GoalVersion: 1, AggregateType: "lease", AggregateID: lease.ID, Actor: project.Actor, OccurredAt: project.OccurredAt, Payload: renewal}, 2)
	if err != nil {
		t.Fatalf("AppendIfUnchanged() error = %v", err)
	}
	if !stored.Sync.LeaseUnconfirmed || stored.Sync.LeaseID != lease.ID {
		t.Fatalf("sync = %#v, want expired lease flag", stored.Sync)
	}
}

func TestRepositoryDerivesRunResumedMetadataFromCurrentRunAndLease(t *testing.T) {
	clock := testkit.Clock{Value: time.Now().UTC()}
	repository := NewRepository(t.TempDir(), nil, ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: "PRJ-1"}, clock)
	state := model.ProjectState{
		Runs:   map[string]model.Run{"RUN-1": {ID: "RUN-1", TaskID: "TSK-1", ContextID: "CTX-1"}},
		Leases: map[string]model.Lease{"LEASE-1": {ID: "LEASE-1", TaskID: "TSK-1", SubjectID: "USR-1", ContextID: "CTX-1", Status: "active", ExpiresAt: clock.Value.Add(time.Hour), AllowedScopes: []string{"internal/teamsync"}}},
	}
	payload, err := json.Marshal(model.RunResumed{RunID: "RUN-1", AdapterCursor: "cursor-1", Reason: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := repository.metadataFor(model.Event{ID: "EVT-RESUME", Type: "run.resumed", Actor: model.Actor{ID: "USR-1"}, Payload: payload}, state, 4, "BATCH-1")
	if err != nil {
		t.Fatalf("metadataFor() error = %v", err)
	}
	if metadata.TaskID != "TSK-1" || metadata.ContextID != "CTX-1" || metadata.LeaseID != "LEASE-1" || len(metadata.AffectedScope) != 1 || metadata.AffectedScope[0] != "internal/teamsync" {
		t.Fatalf("run.resumed metadata = %#v", metadata)
	}
}

func plannedEvent(t *testing.T, project model.Event) model.Event {
	t.Helper()
	return model.Event{ID: "EVT-PLAN", Type: "requirement.planned", ProjectID: project.ProjectID, GoalVersion: 1, AggregateType: "requirement", AggregateID: "REQ-1", Actor: project.Actor, OccurredAt: project.OccurredAt, Payload: []byte(`{"requirement":{"id":"REQ-1","goal_version":1,"title":"Plan","status":"Draft"},"tasks":[{"id":"TSK-1","requirement_id":"REQ-1","goal_version":1,"title":"Task","acceptance_criteria":["pass"],"status":"Draft"}]}`)}
}

func mustEvents(t *testing.T, repository eventstore.Store) []model.Event {
	t.Helper()
	events, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func initializedRepositoryFixture(t *testing.T) (string, eventstore.Store, model.Event, testkit.Clock) {
	t.Helper()
	root := t.TempDir()
	accepted := eventstore.New(root)
	clock := testkit.Clock{Value: time.Now().UTC()}
	owner := model.Actor{ID: "USR-1", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{Root: root, Name: "test", ProjectID: "PRJ-1", Goal: "deliver", CompletionCriteria: []string{"pass"}, Actor: owner}, &testkit.IDs{}, clock); err != nil {
		t.Fatal(err)
	}
	return root, accepted, mustEvents(t, accepted)[0], clock
}

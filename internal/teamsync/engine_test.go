package teamsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/testkit"
)

func TestSyncPullsImportsReconcilesAndPushesInProtocolOrder(t *testing.T) {
	fixture := newSyncFixture(t)
	remoteEvent := fixture.remoteAppend(t, plannedEvent(t, fixture.project))
	localEvent := fixture.appendPending(t, "EVT-LOCAL", "REQ-LOCAL", "TSK-LOCAL")
	remote := &scriptedRemote{
		status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: remoteEvent.Sequence, Writable: true},
		pulls:  [][]model.Event{{remoteEvent}},
		pushes: []scriptedPush{{result: fixture.acceptResult(t, localEvent)}},
		beforePush: func(team.PushBatch) error {
			history, err := fixture.accepted.ReadAll(context.Background())
			if err != nil {
				return err
			}
			if len(history) != 2 || history[1].Sequence != remoteEvent.Sequence || history[1].Hash != remoteEvent.Hash {
				return fmt.Errorf("pulled event was not imported before push: %#v", history)
			}
			return nil
		},
	}

	report, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got, want := remote.calls, []string{"status", "pull:1", "push:BATCH"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("remote call order = %v, want %v", got, want)
	}
	accepted, err := fixture.accepted.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 3 || accepted[1].Hash != remoteEvent.Hash || accepted[1].Sequence != remoteEvent.Sequence {
		t.Fatalf("accepted history did not retain pulled chain: %#v", accepted)
	}
	entries, err := fixture.outbox.ReadAll(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Status != Accepted {
		t.Fatalf("outbox = %#v, %v", entries, err)
	}
	if report.Cursor != 3 || report.Pulled != 1 || report.Accepted != 1 || report.Pending != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestSyncReconcilesPulledMatchingPendingEventWithoutPush(t *testing.T) {
	fixture := newSyncFixture(t)
	pending := fixture.appendPending(t, "EVT-PENDING", "REQ-PENDING", "TSK-PENDING")
	accepted := fixture.remoteAppend(t, pending)
	remote := &scriptedRemote{status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: accepted.Sequence, Writable: true}, pulls: [][]model.Event{{accepted}}}

	report, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got, want := remote.calls, []string{"status", "pull:1"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("remote calls = %v, want %v", got, want)
	}
	entries, err := fixture.outbox.ReadAll(context.Background())
	if err != nil || entries[0].Status != Accepted || entries[0].Result.TeamSeqTo != accepted.Sequence {
		t.Fatalf("outbox = %#v, %v", entries, err)
	}
	if report.Accepted != 1 || report.Pending != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestSyncResponseLostRemainsPendingThenPullReconciles(t *testing.T) {
	fixture := newSyncFixture(t)
	pending := fixture.appendPending(t, "EVT-LOST", "REQ-LOST", "TSK-LOST")
	accepted := fixture.remoteAppend(t, pending)
	remote := &scriptedRemote{
		status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: accepted.Sequence, Writable: true},
		pulls:  [][]model.Event{nil, {accepted}},
		pushes: []scriptedPush{{err: errors.New("response lost")}},
	}
	engine := NewEngine(fixture.root, remote, fixture.accepted, fixture.config)
	if _, err := engine.Sync(context.Background()); !errors.Is(err, ErrOffline) {
		t.Fatalf("first Sync() error = %v, want ErrOffline", err)
	}
	if entries, err := fixture.outbox.ReadAll(context.Background()); err != nil || entries[0].Status != Pending {
		t.Fatalf("after lost response = %#v, %v", entries, err)
	}
	if _, err := engine.Sync(context.Background()); err != nil {
		t.Fatalf("recovery Sync() error = %v", err)
	}
	if got := remote.calls; len(got) != 5 || got[2] != "push:BATCH" || got[3] != "status" || got[4] != "pull:1" {
		t.Fatalf("calls = %v", got)
	}
	if entries, err := fixture.outbox.ReadAll(context.Background()); err != nil || entries[0].Status != Accepted {
		t.Fatalf("after reconciliation = %#v, %v", entries, err)
	}
}

func TestSyncStaleCursorReturnsRetryableWithoutBlindWriteRetry(t *testing.T) {
	fixture := newSyncFixture(t)
	pending := fixture.appendPending(t, "EVT-STALE", "REQ-STALE", "TSK-STALE")
	remote := &scriptedRemote{
		status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: 1, Writable: true},
		pulls:  [][]model.Event{nil},
		pushes: []scriptedPush{{result: team.PushResult{Status: team.PushConflict, Code: team.CodeStaleBaseline, Message: "pull first"}}},
	}
	report, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
	if !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("Sync() error = %v, want ErrStaleCursor", err)
	}
	if len(remote.calls) != 3 || remote.calls[2] != "push:BATCH" {
		t.Fatalf("stale cursor retried write: calls=%v", remote.calls)
	}
	if len(report.Results) != 1 || report.Results[0].Code != team.CodeStaleBaseline || report.Pending != 1 {
		t.Fatalf("report = %#v", report)
	}
	entries, readErr := fixture.outbox.ReadAll(context.Background())
	if readErr != nil || entries[0].Status != Pending || entries[0].Batch.Events[0].ID != pending.ID {
		t.Fatalf("stale result changed outbox = %#v, %v", entries, readErr)
	}
}

func TestSyncStaleCursorResultAndConflictErrorPersistRetryablePending(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.appendPending(t, "EVT-STALE-ERROR", "REQ-STALE-ERROR", "TSK-STALE-ERROR")
	stale := team.PushResult{Status: team.PushConflict, Code: team.CodeStaleBaseline, Message: "pull first"}
	remote := &scriptedRemote{
		status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: 1, Writable: true},
		pulls:  [][]model.Event{nil},
		pushes: []scriptedPush{{result: stale, err: &teamapi.ConflictError{Result: stale}}},
	}
	report, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
	if !errors.Is(err, ErrStaleCursor) || errors.Is(err, ErrOffline) {
		t.Fatalf("Sync() error = %v, want stale cursor without offline", err)
	}
	if len(report.Results) != 1 || !reflect.DeepEqual(report.Results[0], stale) || report.Pending != 1 {
		t.Fatalf("report = %#v", report)
	}
	entries, err := NewOutbox(fixture.root, fixture.config.DeviceID).ReadAll(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Status != Pending || !reflect.DeepEqual(entries[0].Result, stale) {
		t.Fatalf("reopened retryable outbox = %#v, %v", entries, err)
	}
}

func TestSyncReadOnlyStatusDoesNotPullOrPush(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.appendPending(t, "EVT-READONLY", "REQ-READONLY", "TSK-READONLY")
	remote := &scriptedRemote{status: team.Status{ProjectID: fixture.project.ProjectID, Writable: false}}
	if _, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background()); !errors.Is(err, team.ErrNotWritable) {
		t.Fatalf("Sync() error = %v, want team.ErrNotWritable", err)
	}
	if got, want := remote.calls, []string{"status"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("read-only status made remote calls = %v, want %v", got, want)
	}
}

func TestNilEngineSyncDoesNotPanic(t *testing.T) {
	var engine *Engine
	if _, err := engine.Sync(context.Background()); err == nil {
		t.Fatal("nil Engine Sync() succeeded")
	}
}

func TestSyncNetworkFailureIsOfflineAndLeavesOutboxBytesUnchanged(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.appendPending(t, "EVT-NET", "REQ-NET", "TSK-NET")
	before, err := os.ReadFile(fixture.outbox.path)
	if err != nil {
		t.Fatal(err)
	}
	remote := &scriptedRemote{statusErr: errors.New("network down")}
	_, err = NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("Sync() error = %v, want ErrOffline", err)
	}
	after, readErr := os.ReadFile(fixture.outbox.path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("outbox bytes changed: %q, %v", after, readErr)
	}
}

func TestSyncPushesEachBatchWhole(t *testing.T) {
	fixture := newSyncFixture(t)
	plan := plannedEvent(t, fixture.project)
	approved := model.Event{ID: "EVT-APPROVE", Type: "requirement.approved", ProjectID: fixture.project.ProjectID, GoalVersion: 1, AggregateType: "requirement", AggregateID: "REQ-1", Actor: fixture.project.Actor, OccurredAt: fixture.project.OccurredAt, Payload: []byte(`{"requirement_id":"REQ-1"}`)}
	batch := team.PushBatch{BatchID: "BATCH-TWO", BaseTeamSeq: 1, Events: []model.Event{plan, approved}}
	if err := fixture.outbox.Append(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	remote := &scriptedRemote{
		status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: 1, Writable: true},
		pulls:  [][]model.Event{nil},
		pushes: []scriptedPush{{result: team.PushResult{Status: team.PushRejected, Code: "denied"}}},
	}
	if _, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background()); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(remote.pushed) != 1 || remote.pushed[0].BatchID != batch.BatchID || len(remote.pushed[0].Events) != 2 {
		t.Fatalf("pushed batches = %#v", remote.pushed)
	}
}

func TestSyncPersistsRejectedAndConflictResultsBeforeReturning(t *testing.T) {
	for _, expected := range []struct {
		name   string
		result team.PushResult
		status EntryStatus
	}{
		{name: "rejected", result: team.PushResult{Status: team.PushRejected, Code: "invalid"}, status: Rejected},
		{name: "conflict", result: team.PushResult{Status: team.PushConflict, Code: "scope_overlap"}, status: Conflict},
	} {
		t.Run(expected.name, func(t *testing.T) {
			fixture := newSyncFixture(t)
			fixture.appendPending(t, "EVT-"+expected.name, "REQ-"+expected.name, "TSK-"+expected.name)
			remote := &scriptedRemote{status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: 1, Writable: true}, pulls: [][]model.Event{nil}, pushes: []scriptedPush{{result: expected.result}}}
			report, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(context.Background())
			if err != nil {
				t.Fatalf("Sync() error = %v", err)
			}
			entries, err := fixture.outbox.ReadAll(context.Background())
			if err != nil || entries[0].Status != expected.status || entries[0].Result.Code != expected.result.Code {
				t.Fatalf("outbox = %#v, %v", entries, err)
			}
			if len(report.Results) != 1 || report.Results[0].Status != expected.result.Status {
				t.Fatalf("report = %#v", report)
			}
		})
	}
}

func TestSyncCancellationAfterPullKeepsCursorAndOutboxRestartable(t *testing.T) {
	fixture := newSyncFixture(t)
	fixture.appendPending(t, "EVT-CANCEL", "REQ-CANCEL", "TSK-CANCEL")
	pulled := fixture.remoteAppend(t, plannedEvent(t, fixture.project))
	ctx, cancel := context.WithCancel(context.Background())
	remote := &scriptedRemote{status: team.Status{ProjectID: fixture.project.ProjectID, TeamSeq: pulled.Sequence, Writable: true}, pulls: [][]model.Event{{pulled}}, afterPull: cancel}
	if _, err := NewEngine(fixture.root, remote, fixture.accepted, fixture.config).Sync(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync() error = %v, want context.Canceled", err)
	}
	events, err := fixture.accepted.ReadAll(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("cancelled pull changed accepted history: %#v, %v", events, err)
	}
	entries, err := fixture.outbox.ReadAll(context.Background())
	if err != nil || entries[0].Status != Pending {
		t.Fatalf("cancelled pull changed outbox: %#v, %v", entries, err)
	}
	config, err := LoadConfig(context.Background(), fixture.root, fixture.config.DeviceID)
	if !errors.Is(err, os.ErrNotExist) || config.Cursor != 0 {
		t.Fatalf("cancelled pull persisted cursor: %#v, %v", config, err)
	}
}

type scriptedPush struct {
	result team.PushResult
	err    error
}

type scriptedRemote struct {
	status     team.Status
	statusErr  error
	pulls      [][]model.Event
	pushes     []scriptedPush
	calls      []string
	pushed     []team.PushBatch
	afterPull  func()
	beforePush func(team.PushBatch) error
}

func (remote *scriptedRemote) Status(context.Context) (team.Status, error) {
	remote.calls = append(remote.calls, "status")
	return remote.status, remote.statusErr
}

func (remote *scriptedRemote) Pull(_ context.Context, after uint64) ([]model.Event, error) {
	remote.calls = append(remote.calls, fmt.Sprintf("pull:%d", after))
	if len(remote.pulls) == 0 {
		return nil, nil
	}
	result := remote.pulls[0]
	remote.pulls = remote.pulls[1:]
	if remote.afterPull != nil {
		remote.afterPull()
		remote.afterPull = nil
	}
	return result, nil
}

func (remote *scriptedRemote) Push(_ context.Context, batch team.PushBatch) (team.PushResult, error) {
	remote.calls = append(remote.calls, "push:BATCH")
	remote.pushed = append(remote.pushed, batch)
	if remote.beforePush != nil {
		if err := remote.beforePush(batch); err != nil {
			return team.PushResult{}, err
		}
	}
	if len(batch.Events) == 0 {
		return team.PushResult{}, errors.New("empty batch")
	}
	if len(remote.pushes) == 0 {
		return team.PushResult{}, errors.New("unexpected push")
	}
	response := remote.pushes[0]
	remote.pushes = remote.pushes[1:]
	return response.result, response.err
}

type syncFixture struct {
	root     string
	accepted eventstore.Store
	remote   eventstore.Store
	project  model.Event
	config   ClientConfig
	outbox   Outbox
}

func newSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	root := t.TempDir()
	clock := testkit.Clock{Value: time.Now().UTC()}
	owner := model.Actor{ID: "USR-1", Kind: model.ActorHuman, Role: model.RoleOwner}
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{Root: root, Name: "sync", ProjectID: "PRJ-1", Goal: "deliver", CompletionCriteria: []string{"pass"}, Actor: owner}, &testkit.IDs{}, clock); err != nil {
		t.Fatal(err)
	}
	accepted := eventstore.New(root)
	project, err := accepted.ReadAll(context.Background())
	if err != nil || len(project) != 1 {
		t.Fatalf("project events = %#v, %v", project, err)
	}
	remoteRoot := t.TempDir()
	if err := os.MkdirAll(remoteRoot+"/.haowork", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remoteRoot+"/.haowork/events.jsonl", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	remote := eventstore.New(remoteRoot)
	if _, err := remote.Append(context.Background(), project[0]); err != nil {
		t.Fatal(err)
	}
	config := ClientConfig{DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: project[0].ProjectID, Cursor: 1}
	return syncFixture{root: root, accepted: accepted, remote: remote, project: project[0], config: config, outbox: NewOutbox(root, config.DeviceID)}
}

func (fixture syncFixture) appendPending(t *testing.T, eventID, requirementID, taskID string) model.Event {
	t.Helper()
	payload := []byte(`{"requirement":{"id":"` + requirementID + `","goal_version":1,"title":"Plan","status":"Draft"},"tasks":[{"id":"` + taskID + `","requirement_id":"` + requirementID + `","goal_version":1,"title":"Task","acceptance_criteria":["pass"],"status":"Draft"}]}`)
	repository := NewRepository(fixture.root, fixture.accepted, fixture.config, testkit.Clock{Value: time.Now().UTC()})
	event, err := repository.AppendIfUnchanged(context.Background(), model.Event{ID: eventID, Type: "requirement.planned", ProjectID: fixture.project.ProjectID, GoalVersion: 1, AggregateType: "requirement", AggregateID: requirementID, Actor: fixture.project.Actor, OccurredAt: fixture.project.OccurredAt, Payload: payload}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func (fixture syncFixture) remoteAppend(t *testing.T, event model.Event) model.Event {
	t.Helper()
	stored, err := fixture.remote.Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func (fixture syncFixture) acceptResult(t *testing.T, event model.Event) team.PushResult {
	t.Helper()
	stored := fixture.remoteAppend(t, event)
	return team.PushResult{Status: team.PushAccepted, TeamSeqFrom: stored.Sequence, TeamSeqTo: stored.Sequence, Materialized: true, Events: []model.Event{stored}}
}

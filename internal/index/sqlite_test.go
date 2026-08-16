package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
)

func TestRebuildRestoresAggregateHistoryAfterDatabaseDeletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	events := indexEvents(t)

	service := app.New("PRJ-INDEX", 1, fixedHistory{events: events}, nil, nil)
	want, err := service.History(ctx, "REQ-001")
	if err != nil {
		t.Fatalf("read service history: %v", err)
	}

	store, err := Open(root)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if err := store.Rebuild(ctx, events); err != nil {
		t.Fatalf("rebuild index: %v", err)
	}
	got, err := store.SearchHistory(ctx, "REQ-001", 10)
	if err != nil {
		t.Fatalf("search rebuilt history: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate history = %#v, want %#v", got, want)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	databasePath := filepath.Join(root, ".haowork", "index", "local.db")
	if err := os.Remove(databasePath); err != nil {
		t.Fatalf("delete derived database: %v", err)
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("reopen deleted index: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Rebuild(ctx, events); err != nil {
		t.Fatalf("rebuild reopened index: %v", err)
	}
	got, err = reopened.SearchHistory(ctx, "REQ-001", 10)
	if err != nil {
		t.Fatalf("search reopened history: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened aggregate history = %#v, want %#v", got, want)
	}
}

func TestRebuildKeepsNewerSnapshotWhenDelayedRebuildsArriveConcurrently(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	older := indexEvents(t)
	newer := append(append([]model.Event(nil), older...), model.Event{
		Sequence:      5,
		ID:            "EVT-005",
		Type:          "task.started",
		ProjectID:     "PRJ-INDEX",
		GoalVersion:   1,
		AggregateType: "task",
		AggregateID:   "TSK-001",
		Actor:         older[0].Actor,
		OccurredAt:    older[len(older)-1].OccurredAt.Add(time.Minute),
		Payload:       older[1].Payload,
		PreviousHash:  older[len(older)-1].Hash,
		Hash:          "hash-005",
	})
	if err := store.Rebuild(ctx, newer); err != nil {
		t.Fatalf("rebuild newer snapshot: %v", err)
	}

	const rebuilders = 8
	start := make(chan struct{})
	errs := make(chan error, rebuilders)
	var rebuilds sync.WaitGroup
	for range rebuilders {
		rebuilds.Add(1)
		go func() {
			defer rebuilds.Done()
			<-start
			errs <- store.Rebuild(ctx, older)
		}()
	}
	close(start)
	rebuilds.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("rebuild delayed snapshot: %v", err)
		}
	}

	got, err := store.SearchHistory(ctx, "", 0)
	if err != nil {
		t.Fatalf("search rebuilt history: %v", err)
	}
	if !reflect.DeepEqual(got, newer) {
		t.Fatalf("history after concurrent delayed rebuilds = %#v, want %#v", got, newer)
	}
}

type fixedHistory struct {
	events []model.Event
}

func (h fixedHistory) Append(context.Context, model.Event) (model.Event, error) {
	return model.Event{}, nil
}

func (h fixedHistory) AppendIfUnchanged(context.Context, model.Event, int) (model.Event, error) {
	return model.Event{}, nil
}

func (h fixedHistory) ReadAll(context.Context) ([]model.Event, error) {
	return append([]model.Event(nil), h.events...), nil
}

func indexEvents(t *testing.T) []model.Event {
	t.Helper()
	payload, err := json.Marshal(model.RequirementPlanned{
		Requirement: model.Requirement{ID: "REQ-001", Title: "Indexed requirement"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	actor := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	return []model.Event{
		{
			Sequence: 1, ID: "EVT-001", Type: "project.initialized", ProjectID: "PRJ-INDEX", GoalVersion: 1,
			AggregateType: "project", AggregateID: "PRJ-INDEX", Actor: actor,
			OccurredAt: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC), Payload: payload, Hash: "hash-001",
		},
		{
			Sequence: 2, ID: "EVT-002", Type: "requirement.planned", ProjectID: "PRJ-INDEX", GoalVersion: 1,
			AggregateType: "requirement", AggregateID: "REQ-001", Actor: actor,
			OccurredAt: time.Date(2026, time.August, 6, 0, 1, 0, 0, time.UTC), Payload: payload, PreviousHash: "hash-001", Hash: "hash-002",
		},
		{
			Sequence: 3, ID: "EVT-003", Type: "task.planned", ProjectID: "PRJ-INDEX", GoalVersion: 1,
			AggregateType: "task", AggregateID: "TSK-001", Actor: actor,
			OccurredAt: time.Date(2026, time.August, 6, 0, 2, 0, 0, time.UTC), Payload: payload, PreviousHash: "hash-002", Hash: "hash-003",
		},
		{
			Sequence: 4, ID: "EVT-004", Type: "requirement.approved", ProjectID: "PRJ-INDEX", GoalVersion: 1,
			AggregateType: "requirement", AggregateID: "REQ-001", Actor: actor,
			OccurredAt: time.Date(2026, time.August, 6, 0, 3, 0, 0, time.UTC), Payload: payload, PreviousHash: "hash-003", Hash: "hash-004",
		},
	}
}

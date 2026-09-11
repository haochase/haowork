package eventstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestReadAllReadOnlyDoesNotCreateRuntimeLockDirectory(t *testing.T) {
	root := t.TempDir()
	governance := filepath.Join(root, ".haowork")
	if err := os.Mkdir(governance, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.ProjectInitialized{Name: "test", Goal: model.GoalVersion{Version: 1, Statement: "test", CompletionCriteria: []string{"done"}}})
	event := model.Event{ID: "EVT-001", Type: "project.initialized", ProjectID: "PRJ-001", GoalVersion: 1, AggregateType: "project", AggregateID: "PRJ-001", Actor: model.Actor{ID: "USR-001", Kind: model.ActorHuman, Role: model.RoleOwner}, OccurredAt: time.Now().UTC(), Payload: payload}
	store := New(root)
	if err := os.WriteFile(filepath.Join(governance, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(governance, "runtime")); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadAllReadOnly(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if _, err := os.Stat(filepath.Join(governance, "runtime")); !os.IsNotExist(err) {
		t.Fatalf("read-only history access created runtime state: %v", err)
	}
}

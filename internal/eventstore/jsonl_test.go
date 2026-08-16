package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/model"
)

func TestAppendBuildsSequenceAndHashChain(t *testing.T) {
	store := newTestStore(t)
	firstCandidate := testEvent("evt-1", json.RawMessage(`{"action":"created"}`))
	firstCandidate.Sequence = 99
	firstCandidate.PreviousHash = "caller-supplied"
	firstCandidate.Hash = "caller-supplied"
	first, err := store.Append(context.Background(), firstCandidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), testEvent("evt-2", json.RawMessage(`{"action":"updated"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.PreviousHash != "" || first.Hash == "" {
		t.Fatalf("first event = %#v, want sequence 1 with initial hash", first)
	}
	if second.Sequence != 2 || second.PreviousHash != first.Hash || second.Hash == "" {
		t.Fatalf("second event = %#v, want sequence 2 chained to first", second)
	}
}

func TestAppendSameEventIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	candidate := testEvent("evt-1", json.RawMessage(`{"action":"created"}`))
	first, err := store.Append(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("second append = %#v, want %#v", second, first)
	}
	events, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestAppendSameIDWithDifferentPayloadFails(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Append(context.Background(), testEvent("evt-1", json.RawMessage(`{"action":"created"}`))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), testEvent("evt-1", json.RawMessage(`{"action":"changed"}`))); err == nil {
		t.Fatal("Append() succeeded for same ID with a different payload")
	}
}

func TestAppendBatchIfUnchangedWritesAllEventsOrNone(t *testing.T) {
	store := newTestStore(t)
	first := testEvent("evt-1", json.RawMessage(`{"action":"created"}`))
	second := testEvent("evt-2", json.RawMessage(`{"action":"superseded"}`))
	stored, err := store.AppendBatchIfUnchanged(context.Background(), []model.Event{first, second}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].Sequence != 1 || stored[1].Sequence != 2 || stored[1].PreviousHash != stored[0].Hash {
		t.Fatalf("stored batch = %#v, want contiguous hash chain", stored)
	}
	if _, err := store.AppendBatchIfUnchanged(context.Background(), []model.Event{testEvent("evt-3", json.RawMessage(`{}`))}, 0); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("stale batch error = %v, want ErrStateChanged", err)
	}
	events, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("event count after rejected batch = %d, want 2", len(events))
	}
}

func TestAppendBatchIdempotentReturnsOriginalTeamSequences(t *testing.T) {
	store := newTestStore(t)
	candidates := []model.Event{
		testEvent("evt-1", json.RawMessage(`{"action":"created"}`)),
		testEvent("evt-2", json.RawMessage(`{"action":"updated"}`)),
	}
	candidates[0].Sync = &model.SyncMetadata{DeviceID: "device-1", AuthenticatedPrincipal: "alice", EnvironmentID: "team", BatchID: "batch-1"}

	first, err := store.AppendBatchIdempotent(context.Background(), candidates, 0)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.AppendBatchIdempotent(context.Background(), candidates, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry, first) {
		t.Fatalf("retry = %#v, want original stored batch %#v", retry, first)
	}
	if retry[0].Sequence != 1 || retry[1].Sequence != 2 || retry[1].PreviousHash != retry[0].Hash {
		t.Fatalf("retry = %#v, want original team chain sequences", retry)
	}
}

func TestAppendBatchIdempotentRejectsSameIDWithDifferentPayload(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.AppendBatchIdempotent(context.Background(), []model.Event{testEvent("evt-1", json.RawMessage(`{"action":"created"}`))}, 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AppendBatchIdempotent(context.Background(), []model.Event{testEvent("evt-1", json.RawMessage(`{"action":"changed"}`))}, 1)
	if !errors.Is(err, ErrDuplicateDivergence) {
		t.Fatalf("AppendBatchIdempotent() error = %v, want ErrDuplicateDivergence", err)
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("events file changed after rejected divergent retry")
	}
}

func TestImportAcceptedBatchPreservesRemoteSequenceAndHash(t *testing.T) {
	store := newTestStore(t)
	accepted := acceptedBatch(t,
		testEvent("evt-1", json.RawMessage(`{"action":"created"}`)),
		testEvent("evt-2", json.RawMessage(`{"action":"updated"}`)),
	)
	accepted[0].Sync = &model.SyncMetadata{DeviceID: "device-1", AuthenticatedPrincipal: "alice", EnvironmentID: "team", BatchID: "batch-1"}
	accepted = rehashAcceptedBatch(t, accepted)

	stored, err := store.ImportAcceptedBatch(context.Background(), accepted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, accepted) {
		t.Fatalf("stored = %#v, want exact remote accepted batch %#v", stored, accepted)
	}
	fileEvents, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fileEvents, accepted) {
		t.Fatalf("file events = %#v, want exact remote accepted batch %#v", fileEvents, accepted)
	}
}

func TestImportAcceptedBatchAcceptsExactPrefixRetry(t *testing.T) {
	store := newTestStore(t)
	accepted := acceptedBatch(t,
		testEvent("evt-1", json.RawMessage(`{"action":"created"}`)),
		testEvent("evt-2", json.RawMessage(`{"action":"updated"}`)),
	)
	if _, err := store.ImportAcceptedBatch(context.Background(), accepted[:1]); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.ImportAcceptedBatch(context.Background(), accepted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(retry, accepted) {
		t.Fatalf("retry = %#v, want %#v", retry, accepted)
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatal("events file did not retain exact imported prefix")
	}
	events, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, accepted) {
		t.Fatalf("events = %#v, want completed accepted batch %#v", events, accepted)
	}
}

func TestImportAcceptedBatchRejectsDivergentOrPartialChainWithoutWriting(t *testing.T) {
	store := newTestStore(t)
	accepted := acceptedBatch(t,
		testEvent("evt-1", json.RawMessage(`{"action":"created"}`)),
		testEvent("evt-2", json.RawMessage(`{"action":"updated"}`)),
	)
	accepted[1].PreviousHash = "not-the-first-hash"
	accepted[1].Hash = "not-the-second-hash"

	before, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ImportAcceptedBatch(context.Background(), accepted)
	if !errors.Is(err, ErrAcceptedHistoryDivergence) {
		t.Fatalf("ImportAcceptedBatch() error = %v, want ErrAcceptedHistoryDivergence", err)
	}
	after, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("events file changed after rejected partial accepted chain")
	}
}

func TestNewAtKeepsTeamAndMaterializedLogsIndependent(t *testing.T) {
	root := t.TempDir()
	teamPath := filepath.Join(root, "team", "events.jsonl")
	teamLockPath := filepath.Join(root, "team", "events.lock")
	materializedPath := filepath.Join(root, "materialized", "events.jsonl")
	materializedLockPath := filepath.Join(root, "materialized", "events.lock")
	for _, path := range []string{teamPath, materializedPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	teamStore := NewAt(teamPath, teamLockPath)
	materializedStore := NewAt(materializedPath, materializedLockPath)
	if _, err := teamStore.Append(context.Background(), testEvent("team-event", json.RawMessage(`{"scope":"team"}`))); err != nil {
		t.Fatal(err)
	}
	if _, err := materializedStore.Append(context.Background(), testEvent("materialized-event", json.RawMessage(`{"scope":"materialized"}`))); err != nil {
		t.Fatal(err)
	}
	teamEvents, err := teamStore.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	materializedEvents, err := materializedStore.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(teamEvents) != 1 || teamEvents[0].ID != "team-event" {
		t.Fatalf("team events = %#v, want team-only event", teamEvents)
	}
	if len(materializedEvents) != 1 || materializedEvents[0].ID != "materialized-event" {
		t.Fatalf("materialized events = %#v, want materialized-only event", materializedEvents)
	}
}

func TestAppendRejectsEventAtScannerLimitWithoutWriting(t *testing.T) {
	store := newTestStore(t)
	const scannerLineLimit = 16 * 1024 * 1024
	payload := json.RawMessage(`"` + strings.Repeat("x", scannerLineLimit) + `"`)

	if _, err := store.Append(context.Background(), testEvent("evt-too-large", payload)); err == nil || err.Error() != "event exceeds 16 MiB scanner limit" {
		t.Fatalf("Append() error = %v, want deterministic size-limit error", err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("events file length = %d, want 0 after rejected append", len(data))
	}
}

func TestReadAllRejectsModifiedHistoricalLine(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Append(context.Background(), testEvent("evt-1", json.RawMessage(`{"action":"created"}`))); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path, []byte(strings.Replace(string(data), "created", "altered", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAll(context.Background()); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("ReadAll() error = %v, want ErrHistoryCorrupt", err)
	}
}

func TestReadAllRejectsUnknownHistoricalField(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Append(context.Background(), testEvent("evt-1", json.RawMessage(`{"action":"created"}`))); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	data = []byte(line[:len(line)-1] + `,"unexpected":true}` + "\n")
	if err := os.WriteFile(store.path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAll(context.Background()); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("ReadAll() error = %v, want ErrHistoryCorrupt", err)
	}
}

func TestConcurrentAppendProducesContiguousSequence(t *testing.T) {
	store := newTestStore(t)
	const appenders = 20
	var group sync.WaitGroup
	errs := make(chan error, appenders)
	for i := range appenders {
		group.Add(1)
		go func() {
			defer group.Done()
			id := fmt.Sprintf("evt-%d", i)
			_, err := store.Append(context.Background(), testEvent(id, json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))))
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != appenders {
		t.Fatalf("event count = %d, want %d", len(events), appenders)
	}
	ids := make(map[string]struct{}, appenders)
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("sequence at index %d = %d, want %d", index, event.Sequence, index+1)
		}
		if _, exists := ids[event.ID]; exists {
			t.Fatalf("duplicate event ID %q", event.ID)
		}
		ids[event.ID] = struct{}{}
	}
}

func TestReadAllWaitsForWriterLock(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureRuntimeDir(); err != nil {
		t.Fatal(err)
	}
	writer := flock.New(store.lockPath)
	if err := writer.Lock(); err != nil {
		t.Fatal(err)
	}
	defer writer.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := store.ReadAll(ctx); !errors.Is(err, ErrStoreBusy) {
		t.Fatalf("ReadAll() error = %v, want ErrStoreBusy", err)
	}
}

func TestStoreReturnsPreExpiredContextError(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if _, err := store.ReadAll(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadAll() error = %v, want context.DeadlineExceeded", err)
	}
	if _, err := store.Append(ctx, testEvent("evt-1", json.RawMessage(`{"action":"created"}`))); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Append() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestReadAllAcceptsEvidencePayloadLargerThanScannerDefault(t *testing.T) {
	store := newTestStore(t)
	payload, err := json.Marshal(map[string]string{"evidence": strings.Repeat("x", 128*1024)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), testEvent("evt-evidence", payload)); err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Payload) <= 64*1024 {
		t.Fatalf("payload length = %d, want greater than 65536", len(events[0].Payload))
	}
}

func TestReadAllRecreatesDeletedRuntimeDirectory(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Append(context.Background(), testEvent("evt-1", json.RawMessage(`{"action":"created"}`))); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(store.lockPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(store.lockPath)); err != nil {
		t.Fatalf("runtime directory = %v, want recreated", err)
	}
}

func newTestStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".haowork"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".haowork", "events.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return New(root)
}

func testEvent(id string, payload json.RawMessage) model.Event {
	return model.Event{
		ID:            id,
		Type:          "project.created",
		ProjectID:     "PRJ-TEST",
		GoalVersion:   1,
		AggregateType: "project",
		AggregateID:   "PRJ-TEST",
		Actor:         model.Actor{ID: "USR-ALICE", Kind: model.ActorHuman, Role: model.RoleOwner},
		OccurredAt:    time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC),
		Payload:       payload,
	}
}

func acceptedBatch(t *testing.T, events ...model.Event) []model.Event {
	t.Helper()
	return rehashAcceptedBatch(t, events)
}

func rehashAcceptedBatch(t *testing.T, events []model.Event) []model.Event {
	t.Helper()
	prepared := append([]model.Event(nil), events...)
	for index := range prepared {
		prepared[index].Sequence = uint64(index + 1)
		prepared[index].PreviousHash = ""
		if index > 0 {
			prepared[index].PreviousHash = prepared[index-1].Hash
		}
		var err error
		prepared[index].Hash, err = hashEvent(prepared[index])
		if err != nil {
			t.Fatal(err)
		}
	}
	return prepared
}

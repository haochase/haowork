package teamsync

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

func TestOutboxReopenRetainsPendingAndTransitions(t *testing.T) {
	root := t.TempDir()
	outbox := NewOutbox(root, "DEV-1")
	batch := team.PushBatch{BatchID: "BATCH-1", Events: []model.Event{{ID: "EVT-1"}}}
	if err := outbox.Append(context.Background(), batch); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	entries, err := NewOutbox(root, "DEV-1").ReadAll(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Status != Pending {
		t.Fatalf("reopened entries = %#v, %v", entries, err)
	}
	if err := outbox.Update(context.Background(), "BATCH-1", team.PushResult{Status: team.PushAccepted}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	entries, err = outbox.ReadAll(context.Background())
	if err != nil || entries[0].Status != Accepted {
		t.Fatalf("accepted entries = %#v, %v", entries, err)
	}
}

func TestOutboxConcurrentAppendsDoNotLoseEntries(t *testing.T) {
	root := t.TempDir()
	outbox := NewOutbox(root, "DEV-1")
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if err := outbox.Append(context.Background(), team.PushBatch{BatchID: string(rune('A' + i)), Events: []model.Event{{ID: string(rune('a' + i))}}}); err != nil {
				t.Errorf("Append(%d) error = %v", i, err)
			}
		}(i)
	}
	group.Wait()
	entries, err := outbox.ReadAll(context.Background())
	if err != nil || len(entries) != 8 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}

func TestOutboxTerminalStatusIsIdempotentButCannotChange(t *testing.T) {
	outbox := NewOutbox(t.TempDir(), "DEV-1")
	if err := outbox.Append(context.Background(), team.PushBatch{BatchID: "BATCH-1", Events: []model.Event{{ID: "EVT-1"}}}); err != nil {
		t.Fatal(err)
	}
	accepted := team.PushResult{Status: team.PushAccepted, TeamSeqFrom: 1, TeamSeqTo: 1}
	if err := outbox.Update(context.Background(), "BATCH-1", accepted); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Update(context.Background(), "BATCH-1", accepted); err != nil {
		t.Fatalf("idempotent Update() error = %v", err)
	}
	if err := outbox.Update(context.Background(), "BATCH-1", team.PushResult{Status: team.PushRejected}); err == nil {
		t.Fatal("Update() changed terminal status")
	}
}

func TestOutboxRejectsTruncatedExistingRecordWithoutAppendingSuccess(t *testing.T) {
	root := t.TempDir()
	outbox := NewOutbox(root, "DEV-1")
	if err := os.MkdirAll(filepath.Dir(outbox.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outbox.path, []byte(`{"batch":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Append(context.Background(), team.PushBatch{BatchID: "BATCH-1", Events: []model.Event{{ID: "EVT-1"}}}); err == nil {
		t.Fatal("Append() succeeded over a truncated record")
	}
	contents, err := os.ReadFile(outbox.path)
	if err != nil || string(contents) != `{"batch":` {
		t.Fatalf("outbox contents = %q, %v", contents, err)
	}
}

func TestOutboxReplaysPendingBatchesInAppendOrderWhenTimesMatch(t *testing.T) {
	outbox := NewOutbox(t.TempDir(), "DEV-1")
	for _, batchID := range []string{"BATCH-1", "BATCH-2", "BATCH-3"} {
		if err := outbox.Append(context.Background(), team.PushBatch{BatchID: batchID, Events: []model.Event{{ID: batchID}}}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := outbox.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for index, batchID := range []string{"BATCH-1", "BATCH-2", "BATCH-3"} {
		if entries[index].Batch.BatchID != batchID {
			t.Fatalf("entry %d = %q, want %q", index, entries[index].Batch.BatchID, batchID)
		}
	}
}

func TestOutboxRecordRetryableResultKeepsPendingAcrossReopenAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	outbox := NewOutbox(root, "DEV-1")
	if err := outbox.Append(context.Background(), team.PushBatch{BatchID: "BATCH-1", Events: []model.Event{{ID: "EVT-1"}}}); err != nil {
		t.Fatal(err)
	}
	result := team.PushResult{Status: team.PushConflict, Code: team.CodeStaleBaseline, Message: "pull first"}
	if err := outbox.RecordRetryableResult(context.Background(), "BATCH-1", result); err != nil {
		t.Fatalf("RecordRetryableResult() error = %v", err)
	}
	before, err := os.ReadFile(outbox.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.RecordRetryableResult(context.Background(), "BATCH-1", result); err != nil {
		t.Fatalf("idempotent RecordRetryableResult() error = %v", err)
	}
	after, err := os.ReadFile(outbox.path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("idempotent retryable result changed bytes = %q, %v", after, err)
	}
	entries, err := NewOutbox(root, "DEV-1").ReadAll(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Status != Pending || !reflect.DeepEqual(entries[0].Result, result) {
		t.Fatalf("reopened retryable entry = %#v, %v", entries, err)
	}
}

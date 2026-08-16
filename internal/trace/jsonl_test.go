package trace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestTraceAppendUsesInstanceAndSourceEventAsIdempotencyKey(t *testing.T) {
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidate := traceFixture()

	first, err := store.AppendIdempotent(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.AppendIdempotent(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || !reflect.DeepEqual(duplicate, first) {
		t.Fatalf("idempotent append = %#v / %#v, want one stored record", first, duplicate)
	}
	all, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("records = %d, want 1", len(all))
	}
}

func TestTraceRejectsSameExternalKeyWithDifferentContent(t *testing.T) {
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	if _, err := store.AppendIdempotent(context.Background(), traceFixture()); err != nil {
		t.Fatal(err)
	}
	divergent := traceFixture()
	divergent.Status = "failed"
	if _, err := store.AppendIdempotent(context.Background(), divergent); !errors.Is(err, ErrSourceDivergent) {
		t.Fatalf("divergent append error = %v, want ErrSourceDivergent", err)
	}
}

func TestTraceRejectsDuplicateTraceIDWithDifferentExternalSource(t *testing.T) {
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	if _, err := store.AppendIdempotent(context.Background(), traceFixture()); err != nil {
		t.Fatal(err)
	}
	duplicateID := traceFixture()
	duplicateID.SourceEventID = "SRC-OTHER"
	if _, err := store.AppendIdempotent(context.Background(), duplicateID); !errors.Is(err, ErrTraceIDDivergent) {
		t.Fatalf("duplicate trace id error = %v, want ErrTraceIDDivergent", err)
	}
}

func TestTraceRecoversAfterTruncatedTailWithoutAcceptingCorruption(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "execution.jsonl")
	store := NewAt(path, filepath.Join(directory, "execution.lock"))
	if _, err := store.AppendIdempotent(context.Background(), traceFixture()); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`{"sequence":2`)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second := traceFixture()
	second.ID, second.SourceEventID = "TRC-002", "SRC-002"
	if _, err := store.AppendIdempotent(context.Background(), second); err != nil {
		t.Fatalf("append after truncated tail error = %v", err)
	}

	if err := os.WriteFile(path, append(mustReadTraceFile(t, path), []byte("{\"sequence\":99}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAll(context.Background()); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("complete corrupted record error = %v, want ErrHistoryCorrupt", err)
	}
}

func mustReadTraceFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func traceFixture() Envelope {
	return Envelope{
		ID: "TRC-001", MissionID: "MSN-001", GovernanceTaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001",
		LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 2, AgentFunction: model.FunctionBuild,
		EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RoomID: "!team:example.test",
		SourceEventID: "SRC-001", SourceEventType: "skill.policy.decided", SkillName: "patch", SkillVersion: "1.0.0",
		InputSHA256: "input-hash", Status: "allowed", StartedAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
	}
}

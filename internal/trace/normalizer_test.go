package trace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestNormalizerStoresMatrixReferenceAndSummaryHashNotMessageBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.jsonl")
	normalizer := Normalizer{Store: NewAt(path, filepath.Join(t.TempDir(), "execution.lock"))}
	secret := "matrix secret: credential-placeholder-must-not-persist"
	record, err := normalizer.NormalizeMatrix(context.Background(), MatrixEvent{
		ID: "TRC-MATRIX-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001",
		LogicalActorID: "AGT-RESEARCH", RuntimeBindingRevision: 1, AgentFunction: model.FunctionResearch, EnvironmentID: "public", AgentTeamsInstanceID: "AT-001",
		RoomID: "!team:example.test", EventID: "$matrix-001", EventType: "m.room.message", Cursor: "opaque-resume-token", ObservationSequence: 1, Body: secret, OccurredAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.SummarySHA256 == "" || record.SourceEventID != "$matrix-001" || record.SourceEventType != "m.room.message" || record.Cursor != "opaque-resume-token" {
		t.Fatalf("matrix trace = %#v", record)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) {
		t.Fatalf("serialized trace leaked Matrix body: %s", contents)
	}
}

func TestNormalizerRejectsCursorRollbackForSameSource(t *testing.T) {
	normalizer := Normalizer{Store: NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))}
	first := MatrixEvent{ID: "TRC-MATRIX-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-RESEARCH", RuntimeBindingRevision: 1, AgentFunction: model.FunctionResearch, EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RoomID: "!team:example.test", EventID: "$matrix-001", EventType: "m.room.message", Cursor: "opaque-a", ObservationSequence: 1, Body: "first", OccurredAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)}
	if _, err := normalizer.NormalizeMatrix(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.EventID, second.Cursor, second.ObservationSequence, second.Body = "TRC-MATRIX-002", "$matrix-002", "opaque-restored", 0, "second"
	if _, err := normalizer.NormalizeMatrix(context.Background(), second); !errors.Is(err, ErrCursorRollback) {
		t.Fatalf("rollback error = %v, want ErrCursorRollback", err)
	}
}

func TestNormalizerRejectsObservationSequenceGapAndSourceTypeDivergence(t *testing.T) {
	normalizer := Normalizer{Store: NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))}
	first := MatrixEvent{ID: "TRC-MATRIX-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-RESEARCH", RuntimeBindingRevision: 1, AgentFunction: model.FunctionResearch, EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RoomID: "!team:example.test", EventID: "$matrix-001", EventType: "m.room.message", Cursor: "opaque-a", ObservationSequence: 1, Body: "first", OccurredAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)}
	if _, err := normalizer.NormalizeMatrix(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	gap := first
	gap.ID, gap.EventID, gap.Cursor, gap.ObservationSequence = "TRC-MATRIX-003", "$matrix-003", "opaque-c", 3
	if _, err := normalizer.NormalizeMatrix(context.Background(), gap); !errors.Is(err, ErrCursorGap) {
		t.Fatalf("gap error = %v, want ErrCursorGap", err)
	}
	divergent := first
	divergent.EventType = "m.room.redacted"
	if _, err := normalizer.NormalizeMatrix(context.Background(), divergent); !errors.Is(err, ErrSourceDivergent) {
		t.Fatalf("type divergence error = %v, want ErrSourceDivergent", err)
	}
}

func TestNormalizerAtomicallyRejectsSameObservationSequenceForDifferentEvents(t *testing.T) {
	normalizer := Normalizer{Store: NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))}
	first := MatrixEvent{ID: "TRC-MATRIX-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-RESEARCH", RuntimeBindingRevision: 1, AgentFunction: model.FunctionResearch, EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RoomID: "!team:example.test", EventID: "$matrix-001", EventType: "m.room.message", Cursor: "opaque-a", ObservationSequence: 1, Body: "first", OccurredAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)}
	if _, err := normalizer.NormalizeMatrix(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.ID, conflict.EventID, conflict.Cursor = "TRC-MATRIX-OTHER", "$matrix-other", "opaque-other"
	if _, err := normalizer.NormalizeMatrix(context.Background(), conflict); !errors.Is(err, ErrSourceDivergent) {
		t.Fatalf("same observation sequence error = %v, want ErrSourceDivergent", err)
	}
}

func TestNormalizerConcurrentSameObservationSequenceAcceptsOneEvent(t *testing.T) {
	normalizer := Normalizer{Store: NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))}
	base := MatrixEvent{MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-RESEARCH", RuntimeBindingRevision: 1, AgentFunction: model.FunctionResearch, EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RoomID: "!team:example.test", EventType: "m.room.message", Cursor: "opaque", ObservationSequence: 1, Body: "event", OccurredAt: time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)}
	errorsByEvent := make(chan error, 2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		candidate := base
		candidate.ID = "TRC-CONCURRENT-" + string(rune('A'+index))
		candidate.EventID = "$matrix-concurrent-" + string(rune('A'+index))
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for attempt := 0; attempt < 20; attempt++ {
				_, err := normalizer.NormalizeMatrix(context.Background(), candidate)
				if !errors.Is(err, ErrStoreBusy) {
					errorsByEvent <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
			errorsByEvent <- ErrStoreBusy
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByEvent)
	successes, divergences := 0, 0
	for err := range errorsByEvent {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrSourceDivergent) {
			divergences++
		} else {
			t.Fatalf("concurrent append error = %v", err)
		}
	}
	if successes != 1 || divergences != 1 {
		t.Fatalf("success/divergence = %d/%d, want 1/1", successes, divergences)
	}
}

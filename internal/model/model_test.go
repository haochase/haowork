package model

import (
	"encoding/json"
	"testing"
)

func TestP003ModelJSONContractsUseExactSnakeCaseKeys(t *testing.T) {
	actor := Actor{ID: "AGT-001", Kind: ActorAgent, Role: RoleAgent}
	source := ContextSource{Kind: "event", Ref: ".haowork/events.jsonl", Digest: "source-hash", Reason: "required", EventIDs: []string{"EVT-001"}, Excerpt: "relevant event"}
	artifact := ArtifactRef{Kind: "log", URI: "artifacts/check.log", SHA256: "artifact-hash"}
	context := ContextSlice{
		ID: "ctx-1", TaskID: "task-1", GoalVersion: 1, Revision: 2, Summary: "approved context", Sources: []ContextSource{source},
		AllowedPaths: []string{"internal/model"}, DeniedPaths: []string{".env"}, SupersedesID: "ctx-0", SliceHash: "slice-hash", Superseded: true,
	}
	step := Step{ID: "step-1", RunID: "run-1", Kind: "command", Status: "finished", Summary: "run tests", ArtifactRefs: []ArtifactRef{artifact}}
	checkpoint := Checkpoint{ID: "checkpoint-1", RunID: "run-1", StepID: "step-1", ContextHash: "slice-hash", WorkspaceDigest: "workspace-hash", AdapterCursor: "cursor-1"}
	executorEvent := ExecutorEvent{RunID: "run-1", StepID: "step-1", Kind: "stdout", Cursor: "cursor-1", Summary: "tests passed", Artifacts: []ArtifactRef{artifact}}
	evidenceCheck := EvidenceCheck{Name: "go test", Status: "pass", Detail: "all tests passed"}
	run := Run{ID: "run-1", TaskID: "task-1", GoalVersion: 1, Executor: "adapter", ActorID: "AGT-001", ContextID: "ctx-1", ContextHash: "slice-hash", AdapterCursor: "cursor-1", Status: StatusFinished, Result: "implemented"}
	evidence := Evidence{
		ID: "evidence-1", TaskID: "task-1", RunID: "run-1", ContextID: "ctx-1", GoalVersion: 1, Kind: "test", URI: "artifacts/check.log",
		SHA256: "evidence-hash", Outcome: "pass", Status: "verified", Command: "go test ./...", Baseline: "main", Source: "adapter", Actor: actor, Checks: []EvidenceCheck{evidenceCheck},
	}

	tests := []struct {
		name  string
		value any
		keys  []string
	}{
		{"context slice", context, []string{"id", "task_id", "goal_version", "revision", "summary", "sources", "allowed_paths", "denied_paths", "supersedes_id", "slice_hash", "superseded"}},
		{"context source", source, []string{"kind", "ref", "digest", "reason", "event_ids", "excerpt"}},
		{"artifact ref", artifact, []string{"kind", "uri", "sha256"}},
		{"step", step, []string{"id", "run_id", "kind", "status", "summary", "artifact_refs"}},
		{"checkpoint", checkpoint, []string{"id", "run_id", "step_id", "context_hash", "workspace_digest", "adapter_cursor"}},
		{"executor event", executorEvent, []string{"run_id", "step_id", "kind", "cursor", "summary", "artifacts"}},
		{"evidence check", evidenceCheck, []string{"name", "status", "detail"}},
		{"run", run, []string{"id", "task_id", "goal_version", "executor", "actor_id", "context_id", "context_hash", "adapter_cursor", "status", "result"}},
		{"evidence", evidence, []string{"id", "task_id", "run_id", "context_id", "goal_version", "kind", "uri", "sha256", "outcome", "status", "command", "baseline", "source", "actor", "checks"}},
		{"context issued payload", ContextIssued{Context: context}, []string{"context"}},
		{"context superseded payload", ContextSuperseded{ContextID: "ctx-1"}, []string{"context_id"}},
		{"run step started payload", RunStepStarted{Step: step}, []string{"step"}},
		{"run step finished payload", RunStepFinished{Step: step}, []string{"step"}},
		{"checkpoint created payload", CheckpointCreated{Checkpoint: checkpoint}, []string{"checkpoint"}},
		{"executor event received payload", ExecutorEventReceived{ExecutorEvent: executorEvent}, []string{"executor_event"}},
		{"evidence candidate payload", EvidenceCandidateRecorded{Evidence: evidence}, []string{"evidence"}},
		{"evidence verified payload", EvidenceVerified{Evidence: evidence}, []string{"evidence"}},
		{"evidence invalidated payload", EvidenceInvalidated{Evidence: evidence}, []string{"evidence"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]json.RawMessage
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatal(err)
			}
			if len(actual) != len(test.keys) {
				t.Fatalf("JSON keys = %v, want %v", mapKeys(actual), test.keys)
			}
			for _, key := range test.keys {
				if _, exists := actual[key]; !exists {
					t.Fatalf("JSON keys = %v, missing %q", mapKeys(actual), key)
				}
			}
		})
	}
}

func mapKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestExecutorEventLookupRecognizesLegacyCursorAndRejectsAmbiguousKeys(t *testing.T) {
	legacy := ExecutorEvent{RunID: "RUN-001", Cursor: "opaque#000000:$same"}
	newer := ExecutorEvent{RunID: "RUN-001", Cursor: legacy.Cursor, SourceEventID: "$same"}
	events := map[string]ExecutorEvent{ExecutorEventKey(legacy): legacy}
	if exists, err := HasExecutorEvent(events, newer); err != nil || !exists {
		t.Fatalf("HasExecutorEvent() = %t, %v; want legacy match", exists, err)
	}
	events[ExecutorEventKey(newer)] = newer
	if _, err := HasExecutorEvent(events, newer); err == nil {
		t.Fatal("HasExecutorEvent() accepted ambiguous source and legacy keys")
	}
	sourceOnly := map[string]ExecutorEvent{ExecutorEventKey(newer): newer}
	if exists, err := HasExecutorEvent(sourceOnly, legacy); err != nil || !exists {
		t.Fatalf("HasExecutorEvent() = %t, %v; want source-first legacy match", exists, err)
	}
	changedCursor := ExecutorEvent{RunID: "RUN-001", Cursor: "new-token#000000:$same", SourceEventID: "$same"}
	if exists, err := HasExecutorEvent(sourceOnly, changedCursor); err != nil || !exists {
		t.Fatalf("HasExecutorEvent() = %t, %v; want same source with a new cursor match", exists, err)
	}
	wrongSource := ExecutorEvent{RunID: "RUN-001", Cursor: legacy.Cursor, SourceEventID: "$other"}
	if _, err := HasExecutorEvent(map[string]ExecutorEvent{ExecutorEventKey(legacy): legacy}, wrongSource); err == nil {
		t.Fatal("HasExecutorEvent() accepted a different source id for the same legacy cursor")
	}
}

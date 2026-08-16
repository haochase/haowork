package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestVerifierRejectsAgentSelfReportedPass(t *testing.T) {
	root, candidate := verifiedCandidate(t)
	state := contextualState(candidate)
	verifier := NewVerifier(staticState{state: state}, staticScanner{}, recordingRunner{}, root)
	candidate.Command = ""
	candidate.Outcome = "pass"

	decision, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "rejected" {
		t.Fatalf("decision status = %q, want rejected when only the agent outcome says pass", decision.Status)
	}
}

func TestVerifierRequiresCurrentRunContextGoalAndWorkspaceDigest(t *testing.T) {
	root, candidate := verifiedCandidate(t)
	state := contextualState(candidate)
	state.Runs[candidate.RunID] = model.Run{ID: candidate.RunID, TaskID: candidate.TaskID, ContextID: "CTX-NEW", ContextHash: "new-context", GoalVersion: 1, Status: model.StatusFinished}
	verifier := NewVerifier(staticState{state: state}, staticScanner{}, recordingRunner{}, root)

	decision, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "stale" {
		t.Fatalf("decision status = %q, want stale for a superseded run context", decision.Status)
	}
}

func TestVerifierMarksEvidenceStaleAfterWorkspaceOrContextChange(t *testing.T) {
	root, candidate := verifiedCandidate(t)
	state := contextualState(candidate)
	state.Attributions = map[string]model.ChangeAttribution{
		"internal/app/service.go\x00before": {Path: "internal/app/service.go", SHA256: "before", TaskID: candidate.TaskID},
		"internal/app/service.go\x00after":  {Path: "internal/app/service.go", SHA256: "after", TaskID: candidate.TaskID},
	}
	scanner := &sequenceScanner{snapshots: [][]model.FileChange{
		{{Path: "internal/app/service.go", Status: "modified", SHA256: "before", Baseline: "main"}},
		{{Path: "internal/app/service.go", Status: "modified", SHA256: "after", Baseline: "main"}},
	}}
	verifier := NewVerifier(staticState{state: state}, scanner, recordingRunner{}, root)

	first, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "verified" {
		t.Fatalf("first decision status = %q, want verified", first.Status)
	}
	second, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "stale" {
		t.Fatalf("second decision status = %q, want stale after workspace change", second.Status)
	}
}

func TestVerifierRejectsUnattributedWorkspaceChange(t *testing.T) {
	root, candidate := verifiedCandidate(t)
	state := contextualState(candidate)
	verifier := NewVerifier(staticState{state: state}, staticScanner{changes: []model.FileChange{{
		Path: "internal/app/service.go", Status: "modified", SHA256: "unattributed", Baseline: "main",
	}}}, recordingRunner{}, root)

	decision, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "rejected" {
		t.Fatalf("decision status = %q, want rejected for an unattributed change", decision.Status)
	}
}

func TestVerifierRejectsChangeAttributedToAnotherTask(t *testing.T) {
	root, candidate := verifiedCandidate(t)
	state := contextualState(candidate)
	state.Attributions = map[string]model.ChangeAttribution{"api.go\x00hash": {Path: "api.go", SHA256: "hash", TaskID: "TSK-OTHER"}}
	verifier := NewVerifier(staticState{state: state}, staticScanner{changes: []model.FileChange{{Path: "api.go", Status: "modified", SHA256: "hash", Baseline: "main"}}}, recordingRunner{}, root)
	decision, err := verifier.Verify(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", decision.Status)
	}
}

func verifiedCandidate(t *testing.T) (string, EvidenceCandidate) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "evidence.log")
	contents := []byte("tests passed\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return root, EvidenceCandidate{
		TaskID: "TSK-1", RunID: "RUN-1", ContextID: "CTX-1", Kind: "test", URI: path,
		SHA256: hex.EncodeToString(digest[:]), Command: "test-command --check", Outcome: "pass",
		Actor: model.Actor{ID: "AGT-1", Kind: model.ActorAgent, Role: model.RoleAgent},
	}
}

func contextualState(candidate EvidenceCandidate) model.ProjectState {
	return model.ProjectState{
		Goal:     model.GoalVersion{Version: 1},
		Tasks:    map[string]model.Task{candidate.TaskID: {ID: candidate.TaskID, GoalVersion: 1, LastRunID: candidate.RunID, Status: model.StatusVerifying}},
		Runs:     map[string]model.Run{candidate.RunID: {ID: candidate.RunID, TaskID: candidate.TaskID, ContextID: candidate.ContextID, ContextHash: "context-hash", GoalVersion: 1, Status: model.StatusFinished}},
		Contexts: map[string]model.ContextSlice{candidate.ContextID: {ID: candidate.ContextID, TaskID: candidate.TaskID, GoalVersion: 1, SliceHash: "context-hash"}},
	}
}

type staticState struct{ state model.ProjectState }

func (s staticState) Snapshot(context.Context) (model.ProjectState, error) { return s.state, nil }

type staticScanner struct{ changes []model.FileChange }

func (s staticScanner) Scan(context.Context, string) ([]model.FileChange, error) {
	return append([]model.FileChange(nil), s.changes...), nil
}

type sequenceScanner struct {
	snapshots [][]model.FileChange
	index     int
}

func (s *sequenceScanner) Scan(context.Context, string) ([]model.FileChange, error) {
	index := s.index
	if index >= len(s.snapshots) {
		index = len(s.snapshots) - 1
	}
	s.index++
	return append([]model.FileChange(nil), s.snapshots[index]...), nil
}

type recordingRunner struct{}

func (recordingRunner) Run(context.Context, []string, string) (CommandResult, error) {
	return CommandResult{ExitCode: 0, Stdout: "ok"}, nil
}

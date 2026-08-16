package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestCompleteRejectsCandidateStaleAndUnattributedChanges(t *testing.T) {
	t.Run("candidate evidence does not satisfy completion", func(t *testing.T) {
		service, task, run, contextID, root := contextualVerifyingService(t, staticWorkspaceScanner{})
		candidate := recordCandidate(t, service, root, task.ID, run.ID, contextID)
		if candidate.Status != "candidate" {
			t.Fatalf("candidate status = %q, want candidate", candidate.Status)
		}
		if err := service.Complete(context.Background(), task.ID, reviewer("USR-REVIEWER")); !errors.Is(err, ErrGateFailed) {
			t.Fatalf("Complete() candidate error = %v, want ErrGateFailed", err)
		}
	})

	t.Run("new unattributed workspace change blocks a verified candidate", func(t *testing.T) {
		change := model.FileChange{Path: "changed.go", Status: "modified", SHA256: "unattributed", Baseline: "main"}
		scanner := &sequenceWorkspaceScanner{scans: [][]model.FileChange{nil, nil, {change}}}
		service, task, run, contextID, root := contextualVerifyingService(t, scanner)
		service.ConfigureEvidenceVerifier(evidence.NewVerifier(service, scanner, successfulRunner{}, root))
		candidate := recordCandidate(t, service, root, task.ID, run.ID, contextID)
		verified, err := service.VerifyEvidence(context.Background(), candidate.ID, reviewer("USR-REVIEWER"))
		if err != nil {
			t.Fatal(err)
		}
		if verified.Status != "verified" {
			t.Fatalf("verified evidence status = %q, want verified", verified.Status)
		}
		if err := service.Complete(context.Background(), task.ID, reviewer("USR-REVIEWER")); !errors.Is(err, ErrGateFailed) {
			t.Fatalf("Complete() unattributed change error = %v, want ErrGateFailed", err)
		}
	})
}

func TestLegacyVerifyStillAcceptsP002PassEvidence(t *testing.T) {
	service, _, _, task, run := prepareRunningTask(t)
	if err := service.FinishRun(context.Background(), run.ID, "implemented", agent("AGT-001")); err != nil {
		t.Fatal(err)
	}
	evidenceRecord, err := service.Verify(context.Background(), VerifyInput{
		TaskID: task.ID, Kind: "test", URI: "test.log", SHA256: "legacy-hash", Outcome: "pass", Actor: reviewer("USR-REVIEWER"),
	})
	if err != nil {
		t.Fatalf("legacy Verify() error = %v", err)
	}
	if evidenceRecord.Outcome != "pass" {
		t.Fatalf("legacy evidence outcome = %q, want pass", evidenceRecord.Outcome)
	}
}

func TestVerifyEvidencePersistsRejectedDecisionThenReturnsGateFailure(t *testing.T) {
	service, _, task, run, contextID, root := contextualVerifyingServiceWithRepository(t, staticWorkspaceScanner{})
	service.ConfigureEvidenceVerifier(rejectedEvidenceVerifier{})
	candidate := recordCandidate(t, service, root, task.ID, run.ID, contextID)
	updated, err := service.VerifyEvidence(context.Background(), candidate.ID, reviewer("USR-REVIEWER"))
	if !errors.Is(err, ErrGateFailed) {
		t.Fatalf("VerifyEvidence() error = %v, want ErrGateFailed", err)
	}
	if updated.Status != "invalidated" {
		t.Fatalf("status = %q, want invalidated", updated.Status)
	}
	state, statusErr := service.Status(context.Background())
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if state.Evidence[task.ID][0].Status != "invalidated" {
		t.Fatalf("persisted status = %q", state.Evidence[task.ID][0].Status)
	}
}

func TestRecordEvidenceCandidateRejectsInvalidContextBindingWithoutAppending(t *testing.T) {
	service, repository, task, run, contextID, root := contextualVerifyingServiceWithRepository(t, staticWorkspaceScanner{})
	for _, input := range []evidence.EvidenceCandidate{
		{TaskID: task.ID, ContextID: contextID},
		{TaskID: task.ID, RunID: "RUN-STALE", ContextID: contextID},
		{TaskID: task.ID, RunID: run.ID, ContextID: "CTX-STALE"},
	} {
		before := len(repository.events)
		_, err := service.RecordEvidenceCandidate(context.Background(), completeCandidate(t, root, input, run.ID, contextID))
		if !errors.Is(err, ErrGateFailed) {
			t.Fatalf("RecordEvidenceCandidate(%#v) error = %v, want ErrGateFailed", input, err)
		}
		if len(repository.events) != before {
			t.Fatalf("invalid candidate appended an event: got %d, want %d", len(repository.events), before)
		}
	}
}

func completeCandidate(t *testing.T, root string, input evidence.EvidenceCandidate, defaultRunID, defaultContextID string) evidence.EvidenceCandidate {
	t.Helper()
	if input.RunID == "" && input.ContextID != defaultContextID {
		input.RunID = defaultRunID
	}
	if input.ContextID == "" && input.RunID != defaultRunID {
		input.ContextID = defaultContextID
	}
	path := filepath.Join(root, "candidate.log")
	contents := []byte("candidate\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	input.Kind, input.URI, input.SHA256, input.Command, input.Actor = "test", path, hex.EncodeToString(digest[:]), "test-command", agent("AGT-001")
	return input
}

func contextualVerifyingService(t *testing.T, scanner changes.WorkspaceScanner) (*Service, model.Task, model.Run, string, string) {
	service, _, task, run, contextID, root := contextualVerifyingServiceWithRepository(t, scanner)
	return service, task, run, contextID, root
}

func contextualVerifyingServiceWithRepository(t *testing.T, scanner changes.WorkspaceScanner) (*Service, *memoryRepository, model.Task, model.Run, string, string) {
	t.Helper()
	root := t.TempDir()
	repository := initializedRepository(t)
	service := NewWithWorkspaceScanner("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime}, scanner, root)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Evidence gate", Tasks: []TaskInput{{Title: "Verify independently", AcceptanceCriteria: []string{"current evidence only"}}}, Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	contextID := "CTX-CURRENT"
	slice := model.ContextSlice{ID: contextID, TaskID: tasks[0].ID, GoalVersion: 1, SliceHash: "slice-hash"}
	appendEvidenceTestEvent(t, repository, "context.issued", "context", contextID, model.ContextIssued{Context: slice})
	run := model.Run{ID: "RUN-CURRENT", TaskID: tasks[0].ID, GoalVersion: 1, Executor: "codex", ActorID: "AGT-001", ContextID: contextID, ContextHash: slice.SliceHash}
	appendEvidenceTestEvent(t, repository, "run.started", "run", run.ID, model.RunStarted{Run: run})
	appendEvidenceTestEvent(t, repository, "run.finished", "run", run.ID, model.RunFinished{RunID: run.ID, Result: "implemented"})
	return service, repository, tasks[0], run, contextID, root
}

func recordCandidate(t *testing.T, service *Service, root, taskID, runID, contextID string) model.Evidence {
	t.Helper()
	path := filepath.Join(root, "evidence.log")
	contents := []byte("verified output\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	candidate, err := service.RecordEvidenceCandidate(context.Background(), evidence.EvidenceCandidate{
		TaskID: taskID, RunID: runID, ContextID: contextID, Kind: "test", URI: path, SHA256: hex.EncodeToString(digest[:]),
		Command: "test-command --check", Outcome: "pass", Actor: agent("AGT-001"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func appendEvidenceTestEvent(t *testing.T, repository *memoryRepository, eventType, aggregateType, aggregateID string, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events, model.Event{
		ID: "EVT-EVIDENCE-" + eventType, Type: eventType, ProjectID: "PRJ-TEST", GoalVersion: 1,
		AggregateType: aggregateType, AggregateID: aggregateID, Actor: owner("USR-OWNER"), OccurredAt: testTime, Payload: encoded,
	})
}

type successfulRunner struct{}

func (successfulRunner) Run(context.Context, []string, string) (evidence.CommandResult, error) {
	return evidence.CommandResult{ExitCode: 0}, nil
}

type rejectedEvidenceVerifier struct{}

func (rejectedEvidenceVerifier) Verify(context.Context, evidence.EvidenceCandidate) (evidence.EvidenceDecision, error) {
	return evidence.EvidenceDecision{Status: "rejected"}, nil
}

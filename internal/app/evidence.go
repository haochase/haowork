package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/model"
)

// ConfigureEvidenceVerifier replaces the root-bound verifier for controlled integrations and tests.
func (s *Service) ConfigureEvidenceVerifier(verifier evidence.EvidenceVerifier) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.evidenceVerifier = verifier
	s.evidenceVerifierConfigured = true
}

// Snapshot implements evidence.StateProvider without exposing the event count used for writes.
func (s *Service) Snapshot(ctx context.Context) (model.ProjectState, error) {
	state, _, err := s.snapshot(ctx)
	return state, err
}

func (s *Service) RecordEvidenceCandidate(ctx context.Context, input evidence.EvidenceCandidate) (model.Evidence, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.Evidence{}, err
	}
	if strings.TrimSpace(input.Kind) == "" || strings.TrimSpace(input.URI) == "" || !validSHA256(input.SHA256) {
		return model.Evidence{}, errors.New("evidence kind, URI, and SHA-256 are required")
	}
	if err := validateCandidateBinding(state, input); err != nil {
		return model.Evidence{}, fmt.Errorf("%w: %v", ErrGateFailed, err)
	}

	changes, err := s.scanCurrentChanges(ctx, state)
	if err != nil {
		return model.Evidence{}, err
	}
	evidenceID, err := s.ids.New("EVD")
	if err != nil {
		return model.Evidence{}, wrapOperational(err)
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.Evidence{}, wrapOperational(err)
	}
	record := model.Evidence{
		ID: evidenceID, TaskID: input.TaskID, RunID: input.RunID, ContextID: input.ContextID, GoalVersion: state.Goal.Version,
		Kind: input.Kind, URI: input.URI, SHA256: input.SHA256, Command: input.Command, Outcome: input.Outcome,
		Status: "candidate", Baseline: evidenceBaseline(changes), Actor: input.Actor,
	}
	payload, err := json.Marshal(model.EvidenceCandidateRecorded{Evidence: record})
	if err != nil {
		return model.Evidence{}, err
	}
	event, err := s.event(eventID, "evidence.candidate.recorded", "evidence", evidenceID, input.Actor, payload)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.Evidence{}, err
	}
	return record, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

func validateCandidateBinding(state model.ProjectState, input evidence.EvidenceCandidate) error {
	task, exists := state.Tasks[input.TaskID]
	if !exists || task.Status != model.StatusVerifying || task.GoalVersion != state.Goal.Version {
		return errors.New("task is not current and verifying")
	}
	run, exists := state.Runs[input.RunID]
	if !exists || run.TaskID != input.TaskID || run.Status != model.StatusFinished || run.GoalVersion != state.Goal.Version || task.LastRunID != run.ID {
		return errors.New("run is not current and finished")
	}
	slice, exists := state.Contexts[input.ContextID]
	if !exists || slice.TaskID != input.TaskID || slice.GoalVersion != state.Goal.Version || slice.Superseded || run.ContextID != slice.ID || run.ContextHash != slice.SliceHash {
		return errors.New("context does not match the current run")
	}
	return nil
}

func (s *Service) VerifyEvidence(ctx context.Context, evidenceID string, actor model.Actor) (model.Evidence, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.Evidence{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.Evidence{}, err
	}
	record, exists := findEvidence(state, evidenceID)
	if !exists {
		return model.Evidence{}, fmt.Errorf("%w: evidence %q not found", ErrConflict, evidenceID)
	}
	if record.Status != "candidate" {
		return model.Evidence{}, fmt.Errorf("%w: evidence %q status %q requires candidate", ErrConflict, evidenceID, record.Status)
	}
	if s.evidenceVerifier == nil {
		return model.Evidence{}, wrapOperational(errors.New("evidence verifier is not configured"))
	}
	decision, err := s.evidenceVerifier.Verify(ctx, evidence.EvidenceCandidate{
		TaskID: record.TaskID, RunID: record.RunID, ContextID: record.ContextID, Kind: record.Kind, URI: record.URI,
		SHA256: record.SHA256, Command: record.Command, Outcome: record.Outcome, Actor: record.Actor,
	})
	if err != nil {
		return model.Evidence{}, wrapOperational(err)
	}
	updated := record
	updated.Checks = decision.Checks
	updated.Source = decision.Status
	if decision.Status == "verified" {
		updated.Status = "verified"
		payload, err := json.Marshal(model.EvidenceVerified{Evidence: updated})
		if err != nil {
			return model.Evidence{}, err
		}
		if err := s.appendEvent(ctx, eventCount, "evidence.verified", "evidence", evidenceID, actor, payload); err != nil {
			return model.Evidence{}, err
		}
		return updated, nil
	}
	updated.Status = "invalidated"
	payload, err := json.Marshal(model.EvidenceInvalidated{Evidence: updated})
	if err != nil {
		return model.Evidence{}, err
	}
	if err := s.appendEvent(ctx, eventCount, "evidence.invalidated", "evidence", evidenceID, actor, payload); err != nil {
		return model.Evidence{}, err
	}
	return updated, fmt.Errorf("%w: evidence %q %s", ErrGateFailed, evidenceID, decision.Status)
}

func findEvidence(state model.ProjectState, evidenceID string) (model.Evidence, bool) {
	for _, records := range state.Evidence {
		for _, record := range records {
			if record.ID == evidenceID {
				return record, true
			}
		}
	}
	return model.Evidence{}, false
}

func evidenceBaseline(changes []model.FileChange) string {
	if len(changes) == 0 {
		return "clean"
	}
	baseline := changes[0].Baseline
	for _, change := range changes[1:] {
		if change.Baseline != baseline {
			return "mixed"
		}
	}
	return baseline
}

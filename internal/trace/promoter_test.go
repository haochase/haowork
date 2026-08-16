package trace

import (
	"encoding/json"
	"testing"
)

func TestPromoterAllowsCheckpointAndEvidenceCandidateOnly(t *testing.T) {
	for eventType, want := range map[string]CandidateKind{
		"checkpoint.created": CandidateCheckpoint,
		"evidence.candidate": CandidateEvidence,
	} {
		candidate, err := Promote(Envelope{ID: "TRC-001", SourceEventType: eventType, Status: "succeeded", OutputSHA256: "output-hash"}, json.RawMessage(`{"binding":"current"}`))
		if err != nil {
			t.Fatalf("Promote(%q) error = %v", eventType, err)
		}
		if candidate.Kind != want || candidate.TraceID != "TRC-001" || candidate.PayloadSHA256 == "" {
			t.Fatalf("Promote(%q) = %#v", eventType, candidate)
		}
	}
}

func TestPromoterNeverCompletesTaskOrApprovesGoal(t *testing.T) {
	for _, eventType := range []string{"task.completed", "goal.approved", "requirement.approved", "conflict.resolved", "publication.approved"} {
		if _, err := Promote(Envelope{ID: "TRC-001", SourceEventType: eventType, Status: "succeeded", OutputSHA256: "output-hash"}, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("Promote(%q) succeeded, want rejection", eventType)
		}
	}
}

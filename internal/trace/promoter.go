package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var promotable = map[string]CandidateKind{
	"checkpoint.created": CandidateCheckpoint,
	"evidence.candidate": CandidateEvidence,
	"approval.requested": CandidateApproval,
}

// Promote returns an authorization candidate only. It never writes governance facts.
func Promote(record Envelope, payload json.RawMessage) (PromotionCandidate, error) {
	kind, exists := promotable[record.SourceEventType]
	if !exists {
		return PromotionCandidate{}, errors.New("trace event is not promotable")
	}
	if kind == CandidateApproval {
		if record.Status != "waiting" || record.InputSHA256 == "" {
			return PromotionCandidate{}, errors.New("approval trace is not waiting on an input-bound decision")
		}
	} else if record.Status != "succeeded" || record.OutputSHA256 == "" {
		return PromotionCandidate{}, errors.New("trace event is not promotable")
	}
	if !json.Valid(payload) {
		return PromotionCandidate{}, errors.New("promotion payload must be valid JSON")
	}
	digest := sha256.Sum256(payload)
	return PromotionCandidate{
		Kind: kind, TraceID: record.ID, PayloadSHA256: hex.EncodeToString(digest[:]), Payload: append(json.RawMessage(nil), payload...),
	}, nil
}

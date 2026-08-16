package transfer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/haochase/haowork/internal/model"
)

// ApprovalEventReader is the narrow read-only event-store contract required
// to verify a human approval for a return delta.
type ApprovalEventReader interface {
	ReadAll(context.Context) ([]model.Event, error)
}

// EventStoreApprovalVerifier verifies the approval record from the project's
// event store. Callers supply only an approval reference and canonical hash.
type EventStoreApprovalVerifier struct {
	Events ApprovalEventReader
}

func (verifier EventStoreApprovalVerifier) VerifyApproval(ctx context.Context, approvalID, payloadHash string) error {
	if verifier.Events == nil || approvalID == "" || payloadHash == "" {
		return errors.New("approval event store and reference are required")
	}
	events, err := verifier.Events.ReadAll(ctx)
	if err != nil {
		return err
	}
	requested := false
	approved := false
	for _, event := range events {
		if event.AggregateType != "approval" || event.AggregateID != approvalID {
			continue
		}
		switch event.Type {
		case "approval.requested":
			var payload model.ApprovalRequested
			if json.Unmarshal(event.Payload, &payload) == nil && payload.Approval.ID == approvalID && payload.Approval.PayloadSHA256 == payloadHash {
				requested = true
			}
		case "approval.decided":
			var payload model.ApprovalDecided
			if requested && json.Unmarshal(event.Payload, &payload) == nil && payload.ApprovalID == approvalID && payload.PayloadSHA256 == payloadHash && payload.Decision == "approved" && payload.DeciderID == event.Actor.ID && event.Actor.Kind == model.ActorHuman && (event.Actor.Role == model.RoleOwner || event.Actor.Role == model.RoleLead) {
				approved = true
			}
		case "approval.invalidated":
			var payload model.ApprovalInvalidated
			if json.Unmarshal(event.Payload, &payload) == nil && payload.ApprovalID == approvalID {
				approved = false
			}
		}
	}
	if !approved {
		return errors.New("return approval is not approved by an owner or lead")
	}
	return nil
}

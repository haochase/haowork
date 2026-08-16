package team

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

// Policy authorizes an event against the replayed accepted state. It performs
// no mutations, which keeps retries and full-batch preflight deterministic.
type Policy struct{}

func (Policy) Authorize(state model.ProjectState, principal Principal, event model.Event, now time.Time) error {
	if err := validatePrincipalClaims(principal, event); err != nil {
		return err
	}
	if event.Sync.PayloadSHA256 != payloadSHA256(event.Payload) {
		return fmt.Errorf("%w: payload digest does not match event %q", ErrUnauthorized, event.ID)
	}

	switch event.Type {
	case "goal.change.approved":
		if !isHumanOwner(principal.Actor) {
			return fmt.Errorf("%w: only a human owner may approve a goal change", ErrUnauthorized)
		}
		return nil
	case "goal.change.rejected":
		if !isHumanOwner(principal.Actor) {
			return fmt.Errorf("%w: only a human owner may reject a goal change", ErrUnauthorized)
		}
		return nil
	case "requirement.approved":
		if !isHumanApprover(principal.Actor) {
			return fmt.Errorf("%w: requirement approval requires a human approver", ErrUnauthorized)
		}
		return nil
	case "evidence.verified":
		if err := authorizeEvidenceVerification(state, principal, event); err != nil {
			return err
		}
		if requiresAssignedLease(principal) {
			return authorizeAssignedLease(state, principal, event, now)
		}
		return nil
	case "task.completed":
		if !isHumanApprover(principal.Actor) {
			return fmt.Errorf("%w: task completion requires a human approver", ErrUnauthorized)
		}
		return nil
	case "conflict.resolved":
		var payload model.ConflictResolved
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("%w: decode conflict resolution: %v", ErrUnauthorized, err)
		}
		conflict, exists := state.Conflicts[payload.ConflictID]
		if !exists {
			return fmt.Errorf("%w: conflict %q does not exist", ErrUnauthorized, payload.ConflictID)
		}
		return authorizeConflictResolution(principal, conflict)
	case "lease.issued":
		if !isHumanLeadOrOwner(principal.Actor) {
			return fmt.Errorf("%w: only a human owner or lead may issue a lease", ErrUnauthorized)
		}
		return authorizeLeaseIssue(state, event)
	case "lease.revoked":
		if !isHumanLeadOrOwner(principal.Actor) {
			return fmt.Errorf("%w: only a human owner or lead may revoke a lease", ErrUnauthorized)
		}
		return nil
	case "lease.renewed", "lease.released":
		if isHumanLeadOrOwner(principal.Actor) {
			return nil
		}
	}

	if requiresAssignedLease(principal) {
		return authorizeAssignedLease(state, principal, event, now)
	}
	return nil
}

func requiresAssignedLease(principal Principal) bool {
	return principal.Actor.Kind == model.ActorAgent || principal.Actor.Role == model.RoleAgent || principal.Actor.Role == model.RoleContributor
}

func validatePrincipalClaims(principal Principal, event model.Event) error {
	if strings.TrimSpace(principal.AuthenticatedPrincipal) == "" || strings.TrimSpace(principal.DeviceID) == "" || strings.TrimSpace(principal.EnvironmentID) == "" {
		return fmt.Errorf("%w: authenticated principal, device id, and environment id are required", ErrUnauthorized)
	}
	if strings.TrimSpace(principal.Actor.ID) == "" || principal.Actor.Kind == "" || principal.Actor.Role == "" {
		return fmt.Errorf("%w: actor id, kind, and role are required", ErrUnauthorized)
	}
	if event.Sync == nil {
		return fmt.Errorf("%w: %w: event %q is missing sync metadata", ErrUnauthorized, ErrPrincipalMismatch, event.ID)
	}
	if event.Actor != principal.Actor || event.Sync.AuthenticatedPrincipal != principal.AuthenticatedPrincipal || event.Sync.DeviceID != principal.DeviceID || event.Sync.EnvironmentID != principal.EnvironmentID || event.Sync.FunctionalIdentity != principal.FunctionalIdentity {
		return fmt.Errorf("%w: %w: event %q claims a different authenticated actor, device, environment, or functional identity", ErrUnauthorized, ErrPrincipalMismatch, event.ID)
	}
	return nil
}

func authorizeEvidenceVerification(state model.ProjectState, principal Principal, event model.Event) error {
	var payload model.EvidenceVerified
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("%w: decode evidence verification: %v", ErrUnauthorized, err)
	}
	evidence := payload.Evidence
	if accepted, exists := acceptedEvidence(state, evidence.ID); exists {
		evidence = accepted
	}
	if strings.EqualFold(principal.FunctionalIdentity, "verify") && !strings.EqualFold(evidence.Kind, "build") {
		return fmt.Errorf("%w: verify functional identity may only verify Build output", ErrUnauthorized)
	}
	if evidence.Actor.ID == principal.Actor.ID {
		return fmt.Errorf("%w: actor may not verify its own evidence", ErrUnauthorized)
	}
	if run, exists := state.Runs[evidence.RunID]; exists && run.ActorID == principal.Actor.ID {
		return fmt.Errorf("%w: actor may not verify its own run", ErrUnauthorized)
	}
	if principal.Actor.Kind == model.ActorAgent && strings.TrimSpace(evidence.RunID) == "" {
		return fmt.Errorf("%w: agent verification requires an independently attributable run", ErrUnauthorized)
	}
	return nil
}

func acceptedEvidence(state model.ProjectState, evidenceID string) (model.Evidence, bool) {
	for _, records := range state.Evidence {
		for _, evidence := range records {
			if evidence.ID == evidenceID {
				return evidence, true
			}
		}
	}
	return model.Evidence{}, false
}

func authorizeLeaseIssue(state model.ProjectState, event model.Event) error {
	var payload model.LeaseIssued
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("%w: decode lease issue: %v", ErrUnauthorized, err)
	}
	lease := payload.Lease
	if strings.TrimSpace(lease.ID) == "" || strings.TrimSpace(lease.TaskID) == "" || strings.TrimSpace(lease.ContextID) == "" || strings.TrimSpace(lease.SubjectID) == "" || strings.TrimSpace(lease.SubjectKind) == "" || strings.TrimSpace(lease.EnvironmentID) == "" || len(lease.AllowedScopes) == 0 {
		return fmt.Errorf("%w: lease must identify a subject, task, context, environment, and non-empty scope", ErrUnauthorized)
	}
	for _, scope := range lease.AllowedScopes {
		if strings.TrimSpace(scope) == "" {
			return fmt.Errorf("%w: lease scopes must not be blank", ErrUnauthorized)
		}
	}
	if lease.StartsAt.IsZero() || lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(lease.StartsAt) {
		return fmt.Errorf("%w: lease must have a valid time window", ErrUnauthorized)
	}
	if lease.GoalVersion != state.Goal.Version || event.GoalVersion != state.Goal.Version {
		return fmt.Errorf("%w: lease and event goal versions must match current goal version %d", ErrUnauthorized, state.Goal.Version)
	}
	if event.Sync.TaskID != lease.TaskID || event.Sync.ContextID != lease.ContextID {
		return fmt.Errorf("%w: lease issue sync task and context must match the lease", ErrUnauthorized)
	}
	if err := validateCurrentLeaseContext(state, lease); err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	return nil
}

func authorizeAssignedLease(state model.ProjectState, principal Principal, event model.Event, now time.Time) error {
	leaseID := event.Sync.LeaseID
	if strings.TrimSpace(leaseID) == "" {
		return fmt.Errorf("%w: sync lease id is required", ErrLeaseRequired)
	}
	lease, exists := state.Leases[leaseID]
	if !exists || lease.Status != "active" || lease.SubjectID != principal.Actor.ID || lease.SubjectKind != string(principal.Actor.Kind) || lease.GoalVersion != state.Goal.Version {
		return fmt.Errorf("%w: lease %q is not active and assigned to the submitting actor", ErrLeaseRequired, leaseID)
	}
	if event.GoalVersion != lease.GoalVersion {
		return fmt.Errorf("%w: event goal version %d does not match lease %q goal version %d", ErrLeaseRequired, event.GoalVersion, leaseID, lease.GoalVersion)
	}
	if now.Before(lease.StartsAt) || now.After(lease.ExpiresAt) {
		return fmt.Errorf("%w: lease %q is outside its active time window", ErrLeaseRequired, leaseID)
	}
	if err := validateCurrentLeaseContext(state, lease); err != nil {
		return fmt.Errorf("%w: %v", ErrLeaseRequired, err)
	}
	if lease.EnvironmentID != principal.EnvironmentID {
		return fmt.Errorf("%w: lease %q is for environment %q, not %q", ErrLeaseRequired, leaseID, lease.EnvironmentID, principal.EnvironmentID)
	}
	if strings.TrimSpace(event.Sync.TaskID) == "" || event.Sync.TaskID != lease.TaskID {
		return fmt.Errorf("%w: event task %q does not match lease %q task %q", ErrLeaseRequired, event.Sync.TaskID, leaseID, lease.TaskID)
	}
	if strings.TrimSpace(event.Sync.ContextID) == "" || event.Sync.ContextID != lease.ContextID {
		return fmt.Errorf("%w: event context %q does not match lease %q context %q", ErrLeaseRequired, event.Sync.ContextID, leaseID, lease.ContextID)
	}
	if len(event.Sync.AffectedScope) == 0 {
		return fmt.Errorf("%w: event must claim at least one lease scope", ErrLeaseRequired)
	}
	for _, scope := range event.Sync.AffectedScope {
		if strings.TrimSpace(scope) == "" || !contains(lease.AllowedScopes, scope) {
			return fmt.Errorf("%w: scope %q is outside lease %q", ErrLeaseRequired, scope, leaseID)
		}
	}
	if skill := event.Sync.SkillName; skill != "" {
		if !contains(principal.AllowedSkills, skill) || !contains(lease.AllowedSkills, skill) {
			return fmt.Errorf("%w: skill %q is outside principal or lease permissions", ErrLeaseRequired, skill)
		}
	}
	return nil
}

func validateCurrentLeaseContext(state model.ProjectState, lease model.Lease) error {
	if strings.TrimSpace(lease.ContextID) == "" {
		return errors.New("lease context id is required")
	}
	task, exists := state.Tasks[lease.TaskID]
	if !exists || task.GoalVersion != state.Goal.Version {
		return fmt.Errorf("lease task %q is not current for goal version %d", lease.TaskID, state.Goal.Version)
	}
	context, exists := state.Contexts[lease.ContextID]
	if !exists || context.Superseded || context.TaskID != lease.TaskID || context.GoalVersion != state.Goal.Version {
		return fmt.Errorf("lease context %q is not current for task %q and goal version %d", lease.ContextID, lease.TaskID, state.Goal.Version)
	}
	return nil
}

func isHumanOwner(actor model.Actor) bool {
	return actor.Kind == model.ActorHuman && actor.Role == model.RoleOwner
}

func isHumanLeadOrOwner(actor model.Actor) bool {
	return actor.Kind == model.ActorHuman && (actor.Role == model.RoleOwner || actor.Role == model.RoleLead)
}

func isHumanApprover(actor model.Actor) bool {
	return actor.Kind == model.ActorHuman && (actor.Role == model.RoleOwner || actor.Role == model.RoleLead || actor.Role == model.RoleReviewer)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func payloadSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

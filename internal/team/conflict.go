package team

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

const (
	ConflictStaleGoal        = "stale_goal"
	ConflictLeaseReassigned  = "lease_reassigned"
	ConflictScopeOverlap     = "scope_overlap"
	ConflictDesignDiverged   = "design_diverged"
	ConflictEvidenceMismatch = "evidence_mismatch"
	ConflictTerminalState    = "terminal_state"

	AcceptTeam     = "accept_team"
	WithdrawLocal  = "withdraw_local"
	KeepAsProposal = "keep_as_proposal"
	ManualMerge    = "manual_merge"
)

// NormalizeConflictScopes accepts only stable, relative scope identities.
// Paths deliberately use component equality: a/b never overlaps a/beta.
func NormalizeConflictScopes(scopes []string) ([]string, error) {
	normalized := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, raw := range scopes {
		scope := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if scope == "" {
			return nil, fmt.Errorf("conflict scope is required")
		}
		if isScopeIdentity(scope) {
			parts := strings.SplitN(scope, ":", 2)
			if strings.TrimSpace(parts[1]) == "" || strings.Contains(parts[1], "..") || strings.Contains(parts[1], "/") || strings.Contains(parts[1], "\\") {
				return nil, fmt.Errorf("invalid conflict scope %q", raw)
			}
		} else {
			if strings.HasPrefix(scope, "/") || (len(scope) > 1 && scope[1] == ':') {
				return nil, fmt.Errorf("conflict scope %q must be relative", raw)
			}
			for _, component := range strings.Split(scope, "/") {
				if component == ".." {
					return nil, fmt.Errorf("invalid conflict scope %q", raw)
				}
			}
			clean := path.Clean(scope)
			if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
				return nil, fmt.Errorf("invalid conflict scope %q", raw)
			}
			scope = clean
		}
		if _, exists := seen[scope]; !exists {
			seen[scope] = struct{}{}
			normalized = append(normalized, scope)
		}
	}
	return normalized, nil
}

func isScopeIdentity(scope string) bool {
	for _, prefix := range []string{"module:", "task:", "design:", "evidence:"} {
		if strings.HasPrefix(scope, prefix) {
			return true
		}
	}
	return false
}

// DetectConflict evaluates only incompatibilities introduced after a valid
// common base. Candidate validity at that base remains the caller's concern.
func DetectConflict(_ context.Context, current model.ProjectState, principal Principal, batch PushBatch, acceptedSinceBase []model.Event, now time.Time) (*model.Conflict, error) {
	candidateScopes, err := batchScopes(batch.Events)
	if err != nil {
		return nil, err
	}
	if current.Goal.Version != 0 {
		for _, event := range batch.Events {
			if event.GoalVersion != current.Goal.Version {
				return newConflict(ConflictStaleGoal, batch, candidateScopes), nil
			}
		}
	}
	for _, event := range batch.Events {
		if event.Sync == nil || event.Sync.LeaseID == "" {
			continue
		}
		lease, exists := current.Leases[event.Sync.LeaseID]
		if !exists || lease.Status != "active" || lease.SubjectID != principal.Actor.ID || now.Before(lease.StartsAt) || now.After(lease.ExpiresAt) {
			return newConflict(ConflictLeaseReassigned, batch, candidateScopes), nil
		}
	}
	for _, event := range batch.Events {
		if event.Sync != nil && event.Sync.TaskID != "" {
			if task, exists := current.Tasks[event.Sync.TaskID]; exists && (task.Status == model.StatusCompleted || task.Status == model.StatusCancelled) {
				return newConflict(ConflictTerminalState, batch, candidateScopes), nil
			}
		}
	}
	acceptedScopes, err := batchScopes(acceptedSinceBase)
	if err != nil {
		return nil, err
	}
	if evidenceBindingsMismatch(batch.Events) || evidenceBindingsMismatch(acceptedSinceBase) || evidenceContentDiffers(batch.Events, acceptedSinceBase) {
		return newConflict(ConflictEvidenceMismatch, batch, candidateScopes), nil
	}
	if hasIdentityOverlap(candidateScopes, acceptedScopes, "design:") && designContentDiffers(batch.Events, acceptedSinceBase) {
		return newConflict(ConflictDesignDiverged, batch, candidateScopes), nil
	}
	if scopesOverlap(candidateScopes, acceptedScopes) {
		return newConflict(ConflictScopeOverlap, batch, candidateScopes), nil
	}
	return nil, nil
}

func newConflict(kind string, batch PushBatch, scopes []string) *model.Conflict {
	return &model.Conflict{Type: kind, EntityID: batch.BatchID, Status: "open", CommonBase: batch.BaseTeamSeq, LocalVersion: batch.BaseTeamSeq, AffectedScope: scopes, SuggestedActions: suggestedActions(kind), LocalEvents: append([]model.Event(nil), batch.Events...)}
}

func suggestedActions(kind string) []string {
	if kind == ConflictStaleGoal {
		return []string{AcceptTeam, KeepAsProposal, WithdrawLocal}
	}
	return []string{AcceptTeam, KeepAsProposal, ManualMerge, WithdrawLocal}
}

func batchScopes(events []model.Event) ([]string, error) {
	var raw []string
	for _, event := range events {
		if event.Sync != nil {
			raw = append(raw, event.Sync.AffectedScope...)
		}
	}
	return NormalizeConflictScopes(raw)
}

func scopesOverlap(left, right []string) bool {
	set := make(map[string]struct{}, len(left))
	for _, scope := range left {
		if isScopeIdentity(scope) {
			continue
		}
		set[scope] = struct{}{}
	}
	for _, scope := range right {
		if isScopeIdentity(scope) {
			continue
		}
		if _, exists := set[scope]; exists {
			return true
		}
	}
	return false
}

func hasIdentityOverlap(left, right []string, prefix string) bool {
	for _, scope := range left {
		if !strings.HasPrefix(scope, prefix) {
			continue
		}
		for _, other := range right {
			if scope == other {
				return true
			}
		}
	}
	return false
}

func evidenceContentDiffers(candidates, accepted []model.Event) bool {
	local := evidenceIdentities(candidates)
	team := evidenceIdentities(accepted)
	for _, candidate := range local {
		for _, existing := range team {
			if !evidenceIdentitiesMeet(candidate, existing) {
				continue
			}
			if !candidate.valid || !existing.valid || !sameScopes(candidate.scopes, existing.scopes) || candidate.evidence.ID != existing.evidence.ID || candidate.evidence.SHA256 != existing.evidence.SHA256 || candidate.evidence.Baseline != existing.evidence.Baseline {
				return true
			}
		}
	}
	return false
}

func evidenceBindingsMismatch(events []model.Event) bool {
	for _, identity := range evidenceIdentities(events) {
		if !identity.valid {
			return true
		}
		for _, scope := range identity.scopes {
			if identity.evidence.ID != strings.TrimPrefix(scope, "evidence:") {
				return true
			}
		}
	}
	return false
}

type evidenceIdentity struct {
	scopes   []string
	evidence model.Evidence
	valid    bool
}

func evidenceIdentities(events []model.Event) []evidenceIdentity {
	identities := make([]evidenceIdentity, 0, len(events))
	for _, event := range events {
		scopes := normalizedIdentityScopes(event, "evidence:")
		if len(scopes) == 0 {
			continue
		}
		evidence, ok := eventEvidence(event)
		identities = append(identities, evidenceIdentity{scopes: scopes, evidence: evidence, valid: ok})
	}
	return identities
}

func evidenceIdentitiesMeet(left, right evidenceIdentity) bool {
	if scopesOverlapIdentity(left.scopes, right.scopes) {
		return true
	}
	return left.valid && right.valid && left.evidence.ID == right.evidence.ID
}

func scopesOverlapIdentity(left, right []string) bool {
	for _, scope := range left {
		for _, other := range right {
			if scope == other {
				return true
			}
		}
	}
	return false
}

func sameScopes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, scope := range left {
		if !scopesOverlapIdentity([]string{scope}, right) {
			return false
		}
	}
	return true
}

func designContentDiffers(candidates, accepted []model.Event) bool {
	team := designPayloadsByScope(accepted)
	local := designPayloadsByScope(candidates)
	for scope, candidate := range local {
		if accepted, exists := team[scope]; exists && !reflect.DeepEqual(accepted, candidate) {
			return true
		}
	}
	return false
}

func designPayloadsByScope(events []model.Event) map[string]model.RequirementPlanned {
	payloads := make(map[string]model.RequirementPlanned)
	for _, event := range events {
		payload, ok := requirementPlanned(event)
		if !ok {
			continue
		}
		for _, scope := range normalizedIdentityScopes(event, "design:") {
			payloads[scope] = payload
		}
	}
	return payloads
}

func normalizedIdentityScopes(event model.Event, prefix string) []string {
	if event.Sync == nil {
		return nil
	}
	scopes, err := NormalizeConflictScopes(event.Sync.AffectedScope)
	if err != nil {
		return nil
	}
	identities := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if strings.HasPrefix(scope, prefix) {
			identities = append(identities, scope)
		}
	}
	return identities
}

func requirementPlanned(event model.Event) (model.RequirementPlanned, bool) {
	if event.Type != "requirement.planned" {
		return model.RequirementPlanned{}, false
	}
	var payload model.RequirementPlanned
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return model.RequirementPlanned{}, false
	}
	return payload, true
}

func eventEvidence(event model.Event) (model.Evidence, bool) {
	switch event.Type {
	case "evidence.recorded":
		var payload model.EvidenceRecorded
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return model.Evidence{}, false
		}
		return payload.Evidence, true
	case "evidence.candidate.recorded":
		var payload model.EvidenceCandidateRecorded
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return model.Evidence{}, false
		}
		return payload.Evidence, true
	case "evidence.verified":
		var payload model.EvidenceVerified
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return model.Evidence{}, false
		}
		return payload.Evidence, true
	default:
		return model.Evidence{}, false
	}
}

type ConflictResolutionRequest struct {
	ConflictID  string
	Action      string
	Replacement []model.Event
	Confirmed   bool
}

func authorizeConflictResolution(principal Principal, conflict model.Conflict) error {
	if principal.Actor.Kind != model.ActorHuman {
		return fmt.Errorf("%w: agents cannot resolve conflicts", ErrUnauthorized)
	}
	if conflict.Type == ConflictStaleGoal || strings.Contains(conflict.Type, "identity") || strings.Contains(conflict.Type, "migration") || strings.Contains(conflict.Type, "release") {
		if principal.Actor.Role != model.RoleOwner {
			return fmt.Errorf("%w: only an owner may resolve %s", ErrUnauthorized, conflict.Type)
		}
		return nil
	}
	if conflict.Type == ConflictEvidenceMismatch {
		if principal.Actor.Role == model.RoleOwner || principal.Actor.Role == model.RoleLead || principal.Actor.Role == model.RoleReviewer {
			return nil
		}
		return fmt.Errorf("%w: evidence conflict requires reviewer, lead, or owner", ErrUnauthorized)
	}
	if principal.Actor.Role != model.RoleOwner && principal.Actor.Role != model.RoleLead {
		return fmt.Errorf("%w: conflict requires lead or owner", ErrUnauthorized)
	}
	return nil
}

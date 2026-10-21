package model

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// TeamAISourceSnapshot records the verified TeamAI source bound to a mission.
type TeamAISourceSnapshot struct {
	BindingID    string `json:"binding_id"`
	TeamRepo     string `json:"team_repo"`
	TeamRevision string `json:"team_revision"`
	AssetDigest  string `json:"asset_digest"`
}

func BuildTeamAISourceSnapshot(state ProjectState, bindingID string) (TeamAISourceSnapshot, error) {
	binding, exists := state.TeamAIBindings[strings.TrimSpace(bindingID)]
	if !exists {
		return TeamAISourceSnapshot{}, fmt.Errorf("TeamAI binding %q is unknown", bindingID)
	}
	snapshot := normalizeTeamAISourceSnapshot(TeamAISourceSnapshot{BindingID: binding.ID, TeamRepo: binding.TeamRepo, TeamRevision: binding.TeamRevision, AssetDigest: binding.AssetDigest})
	if err := ValidateTeamAISourceSnapshot(state, &snapshot); err != nil {
		return TeamAISourceSnapshot{}, err
	}
	return snapshot, nil
}

// ValidateTeamAISourceSnapshot accepts nil for legacy missions and otherwise
// requires the current project's verified binding to match every source field.
func ValidateTeamAISourceSnapshot(state ProjectState, snapshot *TeamAISourceSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if err := validateTeamAISourceSnapshotFields(*snapshot); err != nil {
		return err
	}
	binding, exists := state.TeamAIBindings[snapshot.BindingID]
	if !exists {
		return fmt.Errorf("TeamAI binding %q is unknown", snapshot.BindingID)
	}
	if binding.ProjectID != state.ProjectID || strings.TrimSpace(state.ProjectID) == "" || binding.ID != snapshot.BindingID {
		return errors.New("TeamAI source binding must belong to the current project")
	}
	if binding.Status != "Ready" || !validHex(binding.AssetDigest, 64) {
		return errors.New("TeamAI source binding must be Ready with a lowercase SHA-256 digest")
	}
	expected := normalizeTeamAISourceSnapshot(TeamAISourceSnapshot{BindingID: binding.ID, TeamRepo: binding.TeamRepo, TeamRevision: binding.TeamRevision, AssetDigest: binding.AssetDigest})
	if *snapshot != expected {
		return errors.New("TeamAI source snapshot does not match the current binding")
	}
	return nil
}

func normalizeTeamAISourceSnapshot(snapshot TeamAISourceSnapshot) TeamAISourceSnapshot {
	snapshot.BindingID = strings.TrimSpace(snapshot.BindingID)
	snapshot.TeamRepo = strings.TrimSpace(snapshot.TeamRepo)
	snapshot.TeamRevision = strings.TrimSpace(snapshot.TeamRevision)
	snapshot.AssetDigest = strings.ToLower(strings.TrimSpace(snapshot.AssetDigest))
	return snapshot
}

func validateTeamAISourceSnapshotFields(snapshot TeamAISourceSnapshot) error {
	if strings.TrimSpace(snapshot.BindingID) == "" || strings.TrimSpace(snapshot.TeamRepo) == "" || strings.TrimSpace(snapshot.TeamRevision) == "" || !validHex(snapshot.AssetDigest, 64) {
		return errors.New("TeamAI source requires binding, repository, revision, and lowercase SHA-256 digest")
	}
	if snapshot != normalizeTeamAISourceSnapshot(snapshot) {
		return errors.New("TeamAI source snapshot is not canonical")
	}
	return nil
}

// TeamAIAssetSpec declares a required asset; it does not import its contents into
// requirements, evidence, decisions, or approvals.
type TeamAIAssetSpec struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

type TeamAIBinding struct {
	ID             string            `json:"id"`
	ProjectID      string            `json:"project_id"`
	TeamRepo       string            `json:"team_repo"`
	TeamRevision   string            `json:"team_revision"`
	Scope          string            `json:"scope"`
	RequiredAssets []TeamAIAssetSpec `json:"required_assets"`
	BoundBy        string            `json:"bound_by"`
	BoundAt        time.Time         `json:"bound_at"`
	Status         string            `json:"status"`
	AssetDigest    string            `json:"asset_digest,omitempty"`
	VerifiedAt     time.Time         `json:"verified_at,omitempty"`
}

type TeamAIBound struct {
	Binding TeamAIBinding `json:"binding"`
}

type TeamAIAssetsVerified struct {
	BindingID   string    `json:"binding_id"`
	AssetDigest string    `json:"asset_digest"`
	VerifiedAt  time.Time `json:"verified_at"`
}

func applyTeamAIBound(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload TeamAIBound
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	binding := payload.Binding
	if event.AggregateType != "teamai_binding" || event.AggregateID != binding.ID {
		return errors.New("TeamAI binding event aggregate does not match payload")
	}
	// This checks the recorded actor attribution, not external authentication.
	if event.Actor.Kind != ActorHuman || (event.Actor.Role != RoleOwner && event.Actor.Role != RoleLead) {
		return errors.New("TeamAI binding requires Human Owner or Lead")
	}
	if strings.TrimSpace(binding.ID) == "" || binding.ID != strings.TrimSpace(binding.ID) || binding.ProjectID != state.ProjectID {
		return errors.New("TeamAI binding identity and current project are required")
	}
	if strings.TrimSpace(event.Actor.ID) == "" || binding.BoundBy != event.Actor.ID {
		return errors.New("TeamAI binding bound_by must match the event actor")
	}
	// TeamRevision is declared metadata. Asset verification does not verify Git HEAD.
	if strings.TrimSpace(binding.TeamRepo) == "" || strings.TrimSpace(binding.TeamRevision) == "" {
		return errors.New("TeamAI repository and declared revision are required")
	}
	if binding.Scope != "project" && binding.Scope != "user" {
		return errors.New("TeamAI binding scope must be project or user")
	}
	if len(binding.RequiredAssets) == 0 || binding.BoundAt.IsZero() {
		return errors.New("TeamAI required assets and binding time are required")
	}
	if (binding.Status != "" && binding.Status != "Bound") || binding.AssetDigest != "" || !binding.VerifiedAt.IsZero() {
		return errors.New("TeamAI bound payload cannot declare verified assets or Ready status")
	}
	binding.Status = "Bound"
	if existing, exists := state.TeamAIBindings[binding.ID]; exists {
		// Compare binding declarations while retaining any later verification state.
		existing.Status = "Bound"
		existing.AssetDigest = ""
		existing.VerifiedAt = time.Time{}
		if reflect.DeepEqual(existing, binding) {
			return nil
		}
		return fmt.Errorf("TeamAI binding %q diverged", binding.ID)
	}
	state.TeamAIBindings[binding.ID] = binding
	return nil
}

func applyTeamAIAssetsVerified(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload TeamAIAssetsVerified
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "teamai_binding" || event.AggregateID != payload.BindingID {
		return errors.New("TeamAI asset verification aggregate does not match payload")
	}
	binding, exists := state.TeamAIBindings[payload.BindingID]
	if !exists {
		return fmt.Errorf("TeamAI binding %q is unknown", payload.BindingID)
	}
	if !validHex(payload.AssetDigest, 64) || payload.VerifiedAt.IsZero() {
		return errors.New("TeamAI asset verification requires lowercase SHA-256 digest and verification time")
	}
	binding.Status = "Ready"
	binding.AssetDigest = payload.AssetDigest
	binding.VerifiedAt = payload.VerifiedAt
	state.TeamAIBindings[binding.ID] = binding
	return nil
}

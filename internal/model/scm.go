package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
)

type SCMRepository struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	Provider          string    `json:"provider"`
	ObjectFormat      string    `json:"object_format"`
	RemoteFingerprint string    `json:"remote_fingerprint,omitempty"`
	RegisteredAt      time.Time `json:"registered_at"`
}

type SCMFileChange struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previous_path,omitempty"`
	Status       string `json:"status"`
	OldBlobOID   string `json:"old_blob_oid,omitempty"`
	NewBlobOID   string `json:"new_blob_oid,omitempty"`
}

type CommitObservation struct {
	RepositoryID         string          `json:"repository_id"`
	CommitOID            string          `json:"commit_oid"`
	TreeOID              string          `json:"tree_oid"`
	ParentOIDs           []string        `json:"parent_oids"`
	AuthorName           string          `json:"author_name"`
	AuthorEmailSHA256    string          `json:"author_email_sha256"`
	CommitterName        string          `json:"committer_name"`
	CommitterEmailSHA256 string          `json:"committer_email_sha256"`
	AuthoredAt           time.Time       `json:"authored_at"`
	CommittedAt          time.Time       `json:"committed_at"`
	Message              string          `json:"message"`
	Changes              []SCMFileChange `json:"changes"`
}

type SCMBinding struct {
	ID            string    `json:"id"`
	RepositoryID  string    `json:"repository_id"`
	CommitOID     string    `json:"commit_oid"`
	ProjectID     string    `json:"project_id"`
	GoalVersion   int       `json:"goal_version"`
	TaskIDs       []string  `json:"task_ids"`
	MissionID     string    `json:"mission_id"`
	EvidenceIDs   []string  `json:"evidence_ids"`
	TraceIDs      []string  `json:"trace_ids"`
	ScopedChanges []string  `json:"scoped_changes"`
	Status        string    `json:"status"`
	ConfirmedBy   string    `json:"confirmed_by,omitempty"`
	ConfirmedAt   time.Time `json:"confirmed_at,omitempty"`
	PolicyVersion string    `json:"policy_version"`
}

type SCMRepositoryRegistered struct {
	Repository SCMRepository `json:"repository"`
}
type SCMCommitObserved struct {
	Observation CommitObservation `json:"observation"`
}
type SCMBindingProposed struct {
	Binding SCMBinding `json:"binding"`
}
type SCMBindingConfirmed struct {
	BindingID   string    `json:"binding_id"`
	ConfirmedBy string    `json:"confirmed_by"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}
type SCMBindingRejected struct {
	BindingID string `json:"binding_id"`
	Reason    string `json:"reason"`
}
type SCMCommitSuperseded struct {
	RepositoryID string `json:"repository_id"`
	CommitOID    string `json:"commit_oid"`
	Reason       string `json:"reason"`
}
type SCMBindingInvalidated struct {
	BindingID string `json:"binding_id"`
	Reason    string `json:"reason"`
}

func SCMCommitKey(repositoryID, commitOID string) string { return repositoryID + "\x00" + commitOID }

func SCMBindingPayloadSHA256(binding SCMBinding) (string, error) {
	copy := binding
	copy.Status = "proposed"
	copy.ConfirmedBy = ""
	copy.ConfirmedAt = time.Time{}
	copy.TaskIDs = canonicalSCMStrings(copy.TaskIDs)
	copy.EvidenceIDs = canonicalSCMStrings(copy.EvidenceIDs)
	copy.TraceIDs = canonicalSCMStrings(copy.TraceIDs)
	copy.ScopedChanges = canonicalSCMStrings(copy.ScopedChanges)
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func applySCMRepositoryRegistered(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload SCMRepositoryRegistered
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	repository := payload.Repository
	if event.AggregateType != "scm_repository" || event.AggregateID != repository.ID {
		return errors.New("SCM repository event aggregate does not match payload")
	}
	if event.Actor.Kind != ActorHuman || event.Actor.Role != RoleOwner {
		return errors.New("SCM repository registration requires Human Owner")
	}
	if strings.TrimSpace(repository.ID) == "" || repository.ProjectID != state.ProjectID || repository.Provider != "local-git" {
		return errors.New("SCM repository identity, project, and provider are required")
	}
	if repository.ObjectFormat != "sha1" && repository.ObjectFormat != "sha256" {
		return errors.New("unsupported SCM object format")
	}
	if repository.RemoteFingerprint != "" && !validHex(repository.RemoteFingerprint, 64) {
		return errors.New("remote fingerprint must be SHA-256")
	}
	if repository.RegisteredAt.IsZero() {
		return errors.New("repository registration time is required")
	}
	if existing, ok := state.SCMRepositories[repository.ID]; ok {
		if reflect.DeepEqual(existing, repository) {
			return nil
		}
		return fmt.Errorf("SCM repository %q diverged", repository.ID)
	}
	state.SCMRepositories[repository.ID] = repository
	return nil
}

func applySCMCommitObserved(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload SCMCommitObserved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	observation := payload.Observation
	repository, exists := state.SCMRepositories[observation.RepositoryID]
	if !exists {
		return fmt.Errorf("SCM repository %q is unknown", observation.RepositoryID)
	}
	if event.AggregateType != "scm_commit" || event.AggregateID != observation.RepositoryID+":"+observation.CommitOID {
		return errors.New("SCM commit event aggregate does not match payload")
	}
	if err := validateCommitObservation(repository, observation); err != nil {
		return err
	}
	key := SCMCommitKey(observation.RepositoryID, observation.CommitOID)
	if existing, ok := state.CommitObservations[key]; ok {
		if reflect.DeepEqual(existing, observation) {
			return nil
		}
		return fmt.Errorf("SCM commit %q diverged", observation.CommitOID)
	}
	state.CommitObservations[key] = observation
	state.SCMCommitStatus[key] = "observed"
	return nil
}

func applySCMBindingProposed(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload SCMBindingProposed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	binding := payload.Binding
	if event.AggregateType != "scm_binding" || event.AggregateID != binding.ID {
		return errors.New("SCM binding event aggregate does not match payload")
	}
	if err := validateSCMBinding(state, binding); err != nil {
		return err
	}
	if existing, ok := state.SCMBindings[binding.ID]; ok {
		if reflect.DeepEqual(existing, binding) {
			return nil
		}
		return fmt.Errorf("SCM binding %q diverged", binding.ID)
	}
	state.SCMBindings[binding.ID] = binding
	return nil
}

func applySCMBindingConfirmed(state *ProjectState, event Event) error {
	var payload SCMBindingConfirmed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "scm_binding" || event.AggregateID != payload.BindingID || payload.ConfirmedBy != event.Actor.ID {
		return errors.New("SCM binding confirmation aggregate or actor does not match payload")
	}
	binding, exists := state.SCMBindings[payload.BindingID]
	if !exists || binding.Status != "proposed" {
		return errors.New("SCM binding is not proposed")
	}
	if payload.ConfirmedAt.IsZero() {
		return errors.New("SCM binding confirmation time is required")
	}
	mission, exists := state.Missions[binding.MissionID]
	if !exists {
		return errors.New("SCM binding mission is missing")
	}
	if err := authorizeSCMConfirmation(state, event.Actor, binding, mission.RiskLevel); err != nil {
		return err
	}
	binding.Status = "confirmed"
	binding.ConfirmedBy = payload.ConfirmedBy
	binding.ConfirmedAt = payload.ConfirmedAt.UTC()
	state.SCMBindings[payload.BindingID] = binding
	return nil
}

func applySCMBindingRejected(state *ProjectState, event Event) error {
	var payload SCMBindingRejected
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "scm_binding" || event.AggregateID != payload.BindingID || strings.TrimSpace(payload.Reason) == "" {
		return errors.New("SCM binding rejection aggregate and reason are required")
	}
	binding, exists := state.SCMBindings[payload.BindingID]
	if !exists || binding.Status != "proposed" {
		return errors.New("SCM binding is not proposed")
	}
	if event.Actor.Kind != ActorHuman || (event.Actor.Role != RoleOwner && event.Actor.Role != RoleLead && event.Actor.Role != RoleReviewer) {
		return errors.New("SCM binding rejection requires Human Owner, Lead, or Reviewer")
	}
	binding.Status = "rejected"
	state.SCMBindings[payload.BindingID] = binding
	return nil
}

func applySCMCommitSuperseded(state *ProjectState, event Event) error {
	var payload SCMCommitSuperseded
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	key := SCMCommitKey(payload.RepositoryID, payload.CommitOID)
	if event.AggregateType != "scm_commit" || event.AggregateID != payload.RepositoryID+":"+payload.CommitOID || strings.TrimSpace(payload.Reason) == "" {
		return errors.New("SCM superseded event aggregate and reason are required")
	}
	if event.Actor.Kind != ActorHuman || (event.Actor.Role != RoleOwner && event.Actor.Role != RoleReviewer) {
		return errors.New("SCM history supersession requires Human Owner or Reviewer")
	}
	if _, exists := state.CommitObservations[key]; !exists {
		return errors.New("SCM commit is unknown")
	}
	if state.SCMCommitStatus[key] == "superseded" {
		return nil
	}
	state.SCMCommitStatus[key] = "superseded"
	return nil
}

func applySCMBindingInvalidated(state *ProjectState, event Event) error {
	var payload SCMBindingInvalidated
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "scm_binding" || event.AggregateID != payload.BindingID || strings.TrimSpace(payload.Reason) == "" {
		return errors.New("SCM binding invalidation aggregate and reason are required")
	}
	if event.Actor.Kind != ActorHuman || (event.Actor.Role != RoleOwner && event.Actor.Role != RoleReviewer) {
		return errors.New("SCM binding invalidation requires Human Owner or Reviewer")
	}
	binding, exists := state.SCMBindings[payload.BindingID]
	if !exists || binding.Status != "confirmed" {
		return errors.New("SCM binding is not confirmed")
	}
	binding.Status = "invalidated"
	state.SCMBindings[payload.BindingID] = binding
	return nil
}

func validateCommitObservation(repository SCMRepository, observation CommitObservation) error {
	length := 40
	if repository.ObjectFormat == "sha256" {
		length = 64
	}
	if !validHex(observation.CommitOID, length) || !validHex(observation.TreeOID, length) {
		return errors.New("commit and tree OIDs do not match repository object format")
	}
	for _, parent := range observation.ParentOIDs {
		if !validHex(parent, length) {
			return errors.New("parent OID does not match repository object format")
		}
	}
	if strings.TrimSpace(observation.AuthorName) == "" || strings.TrimSpace(observation.CommitterName) == "" ||
		!validHex(observation.AuthorEmailSHA256, 64) || !validHex(observation.CommitterEmailSHA256, 64) ||
		observation.AuthoredAt.IsZero() || observation.CommittedAt.IsZero() || strings.TrimSpace(observation.Message) == "" {
		return errors.New("commit author, committer, times, message, and email digests are required")
	}
	seen := make(map[string]struct{}, len(observation.Changes))
	for _, change := range observation.Changes {
		if err := validateSCMChange(change, length); err != nil {
			return err
		}
		if _, exists := seen[change.Path]; exists {
			return fmt.Errorf("duplicate SCM change path %q", change.Path)
		}
		seen[change.Path] = struct{}{}
	}
	return nil
}

func validateSCMChange(change SCMFileChange, oidLength int) error {
	if !validSCMPath(change.Path) || (change.PreviousPath != "" && !validSCMPath(change.PreviousPath)) {
		return errors.New("SCM change path must be repository-relative")
	}
	switch change.Status {
	case "added":
		if change.OldBlobOID != "" || !validHex(change.NewBlobOID, oidLength) {
			return errors.New("added change OIDs are invalid")
		}
	case "deleted":
		if !validHex(change.OldBlobOID, oidLength) || change.NewBlobOID != "" {
			return errors.New("deleted change OIDs are invalid")
		}
	case "modified", "type_changed":
		if !validHex(change.OldBlobOID, oidLength) || !validHex(change.NewBlobOID, oidLength) {
			return errors.New("modified change OIDs are invalid")
		}
	case "renamed", "copied":
		if change.PreviousPath == "" || !validHex(change.OldBlobOID, oidLength) || !validHex(change.NewBlobOID, oidLength) {
			return errors.New("renamed or copied change is invalid")
		}
	default:
		return fmt.Errorf("unsupported SCM change status %q", change.Status)
	}
	return nil
}

func validateSCMBinding(state *ProjectState, binding SCMBinding) error {
	if strings.TrimSpace(binding.ID) == "" || binding.ProjectID != state.ProjectID || binding.GoalVersion != state.Goal.Version || binding.Status != "proposed" || strings.TrimSpace(binding.PolicyVersion) == "" {
		return errors.New("SCM binding identity, project, goal, status, and policy are required")
	}
	key := SCMCommitKey(binding.RepositoryID, binding.CommitOID)
	observation, exists := state.CommitObservations[key]
	if !exists || state.SCMCommitStatus[key] != "observed" {
		return errors.New("SCM binding commit is unavailable")
	}
	if len(binding.TaskIDs) == 0 || !isCanonicalSCMStrings(binding.TaskIDs) {
		return errors.New("SCM binding requires canonical task IDs")
	}
	for _, taskID := range binding.TaskIDs {
		task, ok := state.Tasks[taskID]
		if !ok || task.GoalVersion != state.Goal.Version {
			return fmt.Errorf("SCM binding task %q is unavailable", taskID)
		}
	}
	mission, exists := state.Missions[binding.MissionID]
	if !exists || mission.GoalVersion != state.Goal.Version {
		return errors.New("SCM binding mission is unavailable")
	}
	for _, taskID := range binding.TaskIDs {
		if !containsSCMString(mission.GovernanceTaskIDs, taskID) {
			return fmt.Errorf("SCM binding task %q is outside mission", taskID)
		}
	}
	if len(binding.EvidenceIDs) == 0 {
		return errors.New("SCM binding requires projected evidence")
	}
	if !isCanonicalSCMStrings(binding.EvidenceIDs) || !isCanonicalSCMStrings(binding.TraceIDs) || !isCanonicalSCMStrings(binding.ScopedChanges) || len(binding.ScopedChanges) == 0 {
		return errors.New("SCM binding evidence, trace, and scoped changes must be canonical")
	}
	for _, evidenceID := range binding.EvidenceIDs {
		if !stateHasSCMEvidence(state, binding.TaskIDs, evidenceID) {
			return fmt.Errorf("SCM binding evidence %q is unavailable", evidenceID)
		}
	}
	changePaths := make([]string, 0, len(observation.Changes))
	for _, change := range observation.Changes {
		changePaths = append(changePaths, change.Path)
		if !pathAllowedByMission(change.Path, mission.AllowedScopes) || (change.PreviousPath != "" && !pathAllowedByMission(change.PreviousPath, mission.AllowedScopes)) {
			return fmt.Errorf("SCM change %q is outside mission scope", change.Path)
		}
	}
	if !reflect.DeepEqual(binding.ScopedChanges, canonicalSCMStrings(changePaths)) {
		return errors.New("SCM scoped changes must exactly cover the commit")
	}
	return nil
}

func authorizeSCMConfirmation(state *ProjectState, actor Actor, binding SCMBinding, risk string) error {
	if actor.Kind != ActorHuman {
		return errors.New("SCM binding confirmation requires a Human actor")
	}
	switch risk {
	case "L0", "L1":
		if actor.Role != RoleOwner {
			return errors.New("L0/L1 SCM binding confirmation requires Human Owner")
		}
	case "L2":
		if actor.Role != RoleLead && actor.Role != RoleReviewer {
			return errors.New("L2 SCM binding confirmation requires Human Lead or Reviewer")
		}
		if !hasApprovedSCMBindingRequest(state, binding, risk) {
			return errors.New("L2 SCM binding approval is required")
		}
	case "L3":
		if actor.Role != RoleOwner {
			return errors.New("L3 SCM binding confirmation requires Human Owner")
		}
		if !hasApprovedSCMBindingRequest(state, binding, risk) {
			return errors.New("L3 SCM binding approval is required")
		}
	default:
		return errors.New("SCM binding risk level is invalid")
	}
	return nil
}

func hasApprovedSCMBindingRequest(state *ProjectState, binding SCMBinding, risk string) bool {
	hash, err := SCMBindingPayloadSHA256(binding)
	if err != nil {
		return false
	}
	for _, approval := range state.Approvals {
		if approval.SubjectType == "scm_binding" && approval.SubjectID == binding.ID && approval.PayloadSHA256 == hash && approval.RiskLevel == risk && approval.Status == "approved" {
			return true
		}
	}
	return false
}

func stateHasSCMEvidence(state *ProjectState, taskIDs []string, evidenceID string) bool {
	for _, taskID := range taskIDs {
		for _, evidence := range state.Evidence[taskID] {
			if evidence.ID == evidenceID && evidence.Status != "invalid" {
				return true
			}
		}
	}
	return false
}

func pathAllowedByMission(candidate string, scopes []string) bool {
	for _, scope := range scopes {
		scope = strings.Trim(strings.ReplaceAll(scope, "\\", "/"), "/")
		if candidate == scope || strings.HasPrefix(candidate, scope+"/") {
			return true
		}
	}
	return false
}

func validSCMPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func canonicalSCMStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil
	}
	sort.Strings(result)
	return result
}

func isCanonicalSCMStrings(values []string) bool {
	return reflect.DeepEqual(values, canonicalSCMStrings(values))
}
func containsSCMString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

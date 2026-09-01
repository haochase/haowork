package model

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type SCMRemote struct {
	ID                            string    `json:"id"`
	RepositoryID                  string    `json:"repository_id"`
	Provider                      string    `json:"provider"`
	ProviderRepositoryFingerprint string    `json:"provider_repository_fingerprint"`
	APIHostSHA256                 string    `json:"api_host_sha256"`
	RegisteredAt                  time.Time `json:"registered_at"`
}

type SCMRemoteRefObservation struct {
	RemoteID    string    `json:"remote_id"`
	Ref         string    `json:"ref"`
	CommitOID   string    `json:"commit_oid,omitempty"`
	PreviousOID string    `json:"previous_oid,omitempty"`
	Change      string    `json:"change"`
	ObservedAt  time.Time `json:"observed_at"`
}

type SCMPullRequestObservation struct {
	RemoteID             string     `json:"remote_id"`
	Number               int        `json:"number"`
	State                string     `json:"state"`
	Draft                bool       `json:"draft"`
	TitleSHA256          string     `json:"title_sha256"`
	AuthorSHA256         string     `json:"author_sha256"`
	BaseRef              string     `json:"base_ref"`
	BaseOID              string     `json:"base_oid"`
	HeadRef              string     `json:"head_ref"`
	HeadRepositorySHA256 string     `json:"head_repository_sha256"`
	HeadOID              string     `json:"head_oid"`
	CommitOIDs           []string   `json:"commit_oids"`
	MergeCommitOID       string     `json:"merge_commit_oid,omitempty"`
	MergedAt             *time.Time `json:"merged_at,omitempty"`
	GitHubUpdatedAt      time.Time  `json:"github_updated_at"`
	ObservedAt           time.Time  `json:"observed_at"`
}

type SCMReviewObservation struct {
	RemoteID       string    `json:"remote_id"`
	PullNumber     int       `json:"pull_number"`
	ReviewID       int64     `json:"review_id"`
	CommitOID      string    `json:"commit_oid"`
	ReviewerSHA256 string    `json:"reviewer_sha256"`
	State          string    `json:"state"`
	SubmittedAt    time.Time `json:"submitted_at"`
	ObservedAt     time.Time `json:"observed_at"`
}

type SCMCheckObservation struct {
	RemoteID    string     `json:"remote_id"`
	ExternalID  string     `json:"external_id"`
	Source      string     `json:"source"`
	CommitOID   string     `json:"commit_oid"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ObservedAt  time.Time  `json:"observed_at"`
}

type SCMRemoteRegistered struct {
	Remote SCMRemote `json:"remote"`
}

type SCMRemoteRefObserved struct {
	Observation SCMRemoteRefObservation `json:"observation"`
}

type SCMRemotePullRequestObserved struct {
	Observation SCMPullRequestObservation `json:"observation"`
}

type SCMRemoteReviewObserved struct {
	Observation SCMReviewObservation `json:"observation"`
}

type SCMRemoteCheckObserved struct {
	Observation SCMCheckObservation `json:"observation"`
}

func SCMRemoteRefKey(remoteID, ref string) string {
	return remoteID + "\x00ref\x00" + ref
}

func SCMRemotePullKey(remoteID string, number int) string {
	return remoteID + "\x00pull\x00" + strconv.Itoa(number)
}

func SCMRemoteReviewKey(remoteID string, reviewID int64) string {
	return remoteID + "\x00review\x00" + strconv.FormatInt(reviewID, 10)
}

func SCMRemoteCheckKey(remoteID, externalID string) string {
	return remoteID + "\x00check\x00" + externalID
}

func applySCMRemoteRegistered(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload SCMRemoteRegistered
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	remote := payload.Remote
	if event.AggregateType != "scm_remote" || event.AggregateID != remote.ID {
		return errors.New("SCM remote event aggregate does not match payload")
	}
	if event.Actor.Kind != ActorHuman || event.Actor.Role != RoleOwner {
		return errors.New("SCM remote registration requires Human Owner")
	}
	repository, exists := state.SCMRepositories[remote.RepositoryID]
	if !exists || repository.RemoteFingerprint == "" {
		return errors.New("SCM remote requires a registered repository with remote identity")
	}
	if strings.TrimSpace(remote.ID) == "" || remote.Provider != "github" {
		return errors.New("SCM remote identity and GitHub provider are required")
	}
	if !validHex(remote.ProviderRepositoryFingerprint, 64) || !validHex(remote.APIHostSHA256, 64) {
		return errors.New("SCM remote fingerprints must be SHA-256")
	}
	if remote.RegisteredAt.IsZero() {
		return errors.New("SCM remote registration time is required")
	}
	if existing, ok := state.SCMRemotes[remote.ID]; ok {
		if reflect.DeepEqual(existing, remote) {
			return nil
		}
		return fmt.Errorf("SCM remote %q diverged", remote.ID)
	}
	state.SCMRemotes[remote.ID] = remote
	return nil
}

func applySCMRemoteRefObserved(state *ProjectState, event Event) error {
	var payload SCMRemoteRefObserved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	observation := payload.Observation
	if err := validateRemoteObservationActor(event.Actor); err != nil {
		return err
	}
	repository, err := remoteRepository(state, observation.RemoteID)
	if err != nil {
		return err
	}
	key := SCMRemoteRefKey(observation.RemoteID, observation.Ref)
	if event.AggregateType != "scm_remote_ref" || event.AggregateID != key {
		return errors.New("SCM remote ref aggregate does not match payload")
	}
	if !validRemoteBranchRef(observation.Ref) {
		return errors.New("SCM remote ref must be a fully qualified branch ref")
	}
	if observation.ObservedAt.IsZero() {
		return errors.New("SCM remote ref observation time is required")
	}
	oidLength, err := remoteOIDLength(repository.ObjectFormat)
	if err != nil {
		return err
	}
	switch observation.Change {
	case "created":
		if !validHex(observation.CommitOID, oidLength) || observation.PreviousOID != "" {
			return errors.New("created SCM remote ref requires one current OID")
		}
	case "moved":
		if !validHex(observation.CommitOID, oidLength) || !validHex(observation.PreviousOID, oidLength) || observation.CommitOID == observation.PreviousOID {
			return errors.New("moved SCM remote ref requires different current and previous OIDs")
		}
	case "deleted":
		if observation.CommitOID != "" || !validHex(observation.PreviousOID, oidLength) {
			return errors.New("deleted SCM remote ref requires one previous OID")
		}
	default:
		return fmt.Errorf("unsupported SCM remote ref change %q", observation.Change)
	}
	if existing, ok := state.SCMRemoteRefs[key]; ok {
		if reflect.DeepEqual(existing, observation) {
			return nil
		}
		if !observation.ObservedAt.After(existing.ObservedAt) {
			return errors.New("SCM remote ref observation is stale")
		}
		if observation.PreviousOID != existing.CommitOID {
			return errors.New("SCM remote ref previous OID does not match projection")
		}
	}
	state.SCMRemoteRefs[key] = observation
	return nil
}

func applySCMRemotePullRequestObserved(state *ProjectState, event Event) error {
	var payload SCMRemotePullRequestObserved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	observation := payload.Observation
	if err := validateRemoteObservationActor(event.Actor); err != nil {
		return err
	}
	repository, err := remoteRepository(state, observation.RemoteID)
	if err != nil {
		return err
	}
	key := SCMRemotePullKey(observation.RemoteID, observation.Number)
	if event.AggregateType != "scm_remote_pull" || event.AggregateID != key {
		return errors.New("SCM remote pull request aggregate does not match payload")
	}
	if err := validateRemotePull(repository, observation); err != nil {
		return err
	}
	if existing, ok := state.SCMPullRequests[key]; ok {
		if reflect.DeepEqual(existing, observation) {
			return nil
		}
		if !observation.GitHubUpdatedAt.After(existing.GitHubUpdatedAt) || !observation.ObservedAt.After(existing.ObservedAt) {
			return errors.New("SCM remote pull request observation is stale")
		}
	}
	state.SCMPullRequests[key] = observation
	return nil
}

func applySCMRemoteReviewObserved(state *ProjectState, event Event) error {
	var payload SCMRemoteReviewObserved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	observation := payload.Observation
	if err := validateRemoteObservationActor(event.Actor); err != nil {
		return err
	}
	repository, err := remoteRepository(state, observation.RemoteID)
	if err != nil {
		return err
	}
	if _, exists := state.SCMPullRequests[SCMRemotePullKey(observation.RemoteID, observation.PullNumber)]; !exists {
		return errors.New("SCM remote review requires an observed pull request")
	}
	key := SCMRemoteReviewKey(observation.RemoteID, observation.ReviewID)
	if event.AggregateType != "scm_remote_review" || event.AggregateID != key {
		return errors.New("SCM remote review aggregate does not match payload")
	}
	oidLength, err := remoteOIDLength(repository.ObjectFormat)
	if err != nil {
		return err
	}
	if observation.ReviewID <= 0 || !validHex(observation.CommitOID, oidLength) || !validHex(observation.ReviewerSHA256, 64) {
		return errors.New("SCM remote review identity is invalid")
	}
	if !oneOf(observation.State, "APPROVED", "CHANGES_REQUESTED", "COMMENTED", "DISMISSED", "PENDING") {
		return fmt.Errorf("unsupported SCM remote review state %q", observation.State)
	}
	if observation.SubmittedAt.IsZero() || observation.ObservedAt.Before(observation.SubmittedAt) {
		return errors.New("SCM remote review timestamps are invalid")
	}
	if existing, ok := state.SCMReviews[key]; ok {
		if reflect.DeepEqual(existing, observation) {
			return nil
		}
		if !observation.ObservedAt.After(existing.ObservedAt) {
			return errors.New("SCM remote review observation is stale")
		}
	}
	state.SCMReviews[key] = observation
	return nil
}

func applySCMRemoteCheckObserved(state *ProjectState, event Event) error {
	var payload SCMRemoteCheckObserved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	observation := payload.Observation
	if err := validateRemoteObservationActor(event.Actor); err != nil {
		return err
	}
	repository, err := remoteRepository(state, observation.RemoteID)
	if err != nil {
		return err
	}
	key := SCMRemoteCheckKey(observation.RemoteID, observation.ExternalID)
	if event.AggregateType != "scm_remote_check" || event.AggregateID != key {
		return errors.New("SCM remote check aggregate does not match payload")
	}
	oidLength, err := remoteOIDLength(repository.ObjectFormat)
	if err != nil {
		return err
	}
	if strings.TrimSpace(observation.ExternalID) == "" || strings.TrimSpace(observation.Name) == "" || !validHex(observation.CommitOID, oidLength) {
		return errors.New("SCM remote check identity is invalid")
	}
	if !oneOf(observation.Source, "check-run", "commit-status") {
		return fmt.Errorf("unsupported SCM remote check source %q", observation.Source)
	}
	if !oneOf(observation.Status, "queued", "in_progress", "completed", "requested", "waiting", "pending") {
		return fmt.Errorf("unsupported SCM remote check status %q", observation.Status)
	}
	if !oneOf(observation.Conclusion, "", "action_required", "cancelled", "error", "failure", "neutral", "skipped", "stale", "startup_failure", "success", "timed_out") {
		return fmt.Errorf("unsupported SCM remote check conclusion %q", observation.Conclusion)
	}
	if observation.Status == "completed" && observation.Conclusion == "" {
		return errors.New("completed SCM remote check requires a conclusion")
	}
	if observation.Status != "completed" && observation.Conclusion != "" {
		return errors.New("incomplete SCM remote check cannot have a conclusion")
	}
	if observation.ObservedAt.IsZero() || (observation.StartedAt != nil && observation.ObservedAt.Before(*observation.StartedAt)) || (observation.CompletedAt != nil && observation.ObservedAt.Before(*observation.CompletedAt)) {
		return errors.New("SCM remote check timestamps are invalid")
	}
	if existing, ok := state.SCMChecks[key]; ok {
		if reflect.DeepEqual(existing, observation) {
			return nil
		}
		if !observation.ObservedAt.After(existing.ObservedAt) {
			return errors.New("SCM remote check observation is stale")
		}
		if existing.Status == "completed" && observation.Status != "completed" {
			return errors.New("completed SCM remote check cannot regress")
		}
	}
	state.SCMChecks[key] = observation
	return nil
}

func remoteRepository(state *ProjectState, remoteID string) (SCMRepository, error) {
	remote, exists := state.SCMRemotes[remoteID]
	if !exists {
		return SCMRepository{}, fmt.Errorf("SCM remote %q is unknown", remoteID)
	}
	repository, exists := state.SCMRepositories[remote.RepositoryID]
	if !exists {
		return SCMRepository{}, fmt.Errorf("SCM repository %q is unknown", remote.RepositoryID)
	}
	return repository, nil
}

func validateRemotePull(repository SCMRepository, observation SCMPullRequestObservation) error {
	if observation.Number <= 0 || !oneOf(observation.State, "open", "closed") {
		return errors.New("SCM remote pull request number and state are invalid")
	}
	if !validHex(observation.TitleSHA256, 64) || !validHex(observation.AuthorSHA256, 64) || !validHex(observation.HeadRepositorySHA256, 64) {
		return errors.New("SCM remote pull request identities must be SHA-256")
	}
	if !validRemoteBranchRef(observation.BaseRef) || !validRemoteBranchRef(observation.HeadRef) {
		return errors.New("SCM remote pull request refs must be fully qualified branches")
	}
	oidLength, err := remoteOIDLength(repository.ObjectFormat)
	if err != nil {
		return err
	}
	if !validHex(observation.BaseOID, oidLength) || !validHex(observation.HeadOID, oidLength) {
		return errors.New("SCM remote pull request base and head OIDs are invalid")
	}
	observation.CommitOIDs = canonicalRemoteOIDs(observation.CommitOIDs)
	if len(observation.CommitOIDs) == 0 || !isCanonicalRemoteOIDs(observation.CommitOIDs) || !containsSCMString(observation.CommitOIDs, observation.HeadOID) {
		return errors.New("SCM remote pull request commits must be canonical and include head")
	}
	for _, oid := range observation.CommitOIDs {
		if !validHex(oid, oidLength) {
			return errors.New("SCM remote pull request contains an invalid commit OID")
		}
	}
	if observation.MergeCommitOID != "" && !validHex(observation.MergeCommitOID, oidLength) {
		return errors.New("SCM remote pull request merge OID is invalid")
	}
	if observation.MergedAt != nil && observation.State != "closed" {
		return errors.New("merged SCM remote pull request must be closed")
	}
	if observation.MergedAt == nil && observation.State == "open" && observation.MergeCommitOID != "" {
		return errors.New("open SCM remote pull request cannot have a merge commit")
	}
	if observation.GitHubUpdatedAt.IsZero() || observation.ObservedAt.Before(observation.GitHubUpdatedAt) {
		return errors.New("SCM remote pull request timestamps are invalid")
	}
	return nil
}

func validateRemoteObservationActor(actor Actor) error {
	if actor.Kind != ActorHuman || !oneOf(string(actor.Role), string(RoleOwner), string(RoleLead), string(RoleReviewer)) {
		return errors.New("SCM remote observation requires Human Owner, Lead, or Reviewer")
	}
	return nil
}

func remoteOIDLength(format string) (int, error) {
	switch format {
	case "sha1":
		return 40, nil
	case "sha256":
		return 64, nil
	default:
		return 0, fmt.Errorf("unsupported SCM object format %q", format)
	}
}

func validRemoteBranchRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("/._-", character) {
			continue
		}
		return false
	}
	return true
}

func canonicalRemoteOIDs(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return nil
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] == result[write-1] {
			continue
		}
		result[write] = result[read]
		write++
	}
	return result[:write]
}

func isCanonicalRemoteOIDs(values []string) bool {
	return reflect.DeepEqual(values, canonicalRemoteOIDs(values))
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

package model

import (
	"encoding/json"
	"time"
)

type ActorKind string
type ActorRole string

const (
	ActorHuman ActorKind = "human"
	ActorAgent ActorKind = "agent"

	RoleOwner       ActorRole = "owner"
	RoleLead        ActorRole = "lead"
	RoleContributor ActorRole = "contributor"
	RoleReviewer    ActorRole = "reviewer"
	RoleAgent       ActorRole = "agent"
)

type Actor struct {
	ID   string    `json:"id"`
	Kind ActorKind `json:"kind"`
	Role ActorRole `json:"role"`
}

type Event struct {
	Sequence      uint64          `json:"sequence"`
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	ProjectID     string          `json:"project_id"`
	GoalVersion   int             `json:"goal_version"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Actor         Actor           `json:"actor"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
	Sync          *SyncMetadata   `json:"sync,omitempty"`
	PreviousHash  string          `json:"previous_hash,omitempty"`
	Hash          string          `json:"hash"`
}

type Status string

const (
	StatusDraft     Status = "Draft"
	StatusApproved  Status = "Approved"
	StatusRunning   Status = "Running"
	StatusVerifying Status = "Verifying"
	StatusVerified  Status = "Verified"
	StatusCompleted Status = "Completed"
	StatusFinished  Status = "Finished"
	StatusPaused    Status = "Paused"
	StatusFailed    Status = "Failed"
	StatusCancelled Status = "Cancelled"
)

type GoalVersion struct {
	Version            int      `json:"version"`
	Statement          string   `json:"statement"`
	Invariants         []string `json:"invariants"`
	CompletionCriteria []string `json:"completion_criteria"`
}

type SyncMetadata struct {
	DeviceID               string    `json:"device_id"`
	AuthenticatedPrincipal string    `json:"authenticated_principal"`
	FunctionalIdentity     string    `json:"functional_identity,omitempty"`
	EnvironmentID          string    `json:"environment_id"`
	BaseTeamSeq            uint64    `json:"base_team_seq"`
	BatchID                string    `json:"batch_id"`
	TaskID                 string    `json:"task_id,omitempty"`
	ContextID              string    `json:"context_id,omitempty"`
	LeaseID                string    `json:"lease_id,omitempty"`
	SkillName              string    `json:"skill_name,omitempty"`
	SkillVersion           string    `json:"skill_version,omitempty"`
	TraceID                string    `json:"trace_id"`
	PayloadSHA256          string    `json:"payload_sha256"`
	AffectedScope          []string  `json:"affected_scope,omitempty"`
	LeaseUnconfirmed       bool      `json:"lease_unconfirmed,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

type GoalChange struct {
	ID          string      `json:"id"`
	Reason      string      `json:"reason"`
	Status      string      `json:"status"`
	ProposerID  string      `json:"proposer_id"`
	DeciderID   string      `json:"decider_id"`
	BaseVersion int         `json:"base_version"`
	Proposed    GoalVersion `json:"proposed"`
	CreatedAt   time.Time   `json:"created_at"`
	DecidedAt   time.Time   `json:"decided_at"`
}

type Lease struct {
	ID                 string    `json:"id"`
	TaskID             string    `json:"task_id"`
	SubjectKind        string    `json:"subject_kind"`
	SubjectID          string    `json:"subject_id"`
	EnvironmentID      string    `json:"environment_id"`
	AgentTeamsInstance string    `json:"agent_teams_instance"`
	ContextID          string    `json:"context_id"`
	GoalVersion        int       `json:"goal_version"`
	Revision           int       `json:"revision"`
	AllowedScopes      []string  `json:"allowed_scopes"`
	AllowedSkills      []string  `json:"allowed_skills"`
	RiskLevel          string    `json:"risk_level"`
	Status             string    `json:"status"`
	StartsAt           time.Time `json:"starts_at"`
	ExpiresAt          time.Time `json:"expires_at"`
}

type Conflict struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	EntityID         string    `json:"entity_id"`
	Status           string    `json:"status"`
	ResolverID       string    `json:"resolver_id"`
	Resolution       string    `json:"resolution"`
	CommonBase       uint64    `json:"common_base"`
	TeamVersion      uint64    `json:"team_version"`
	LocalVersion     uint64    `json:"local_version"`
	AffectedScope    []string  `json:"affected_scope"`
	SuggestedActions []string  `json:"suggested_actions"`
	LocalEvents      []Event   `json:"local_events"`
	CreatedAt        time.Time `json:"created_at"`
	ResolvedAt       time.Time `json:"resolved_at"`
}

type Requirement struct {
	ID          string   `json:"id"`
	GoalVersion int      `json:"goal_version"`
	Title       string   `json:"title"`
	Constraints []string `json:"constraints"`
	Status      Status   `json:"status"`
}

type Task struct {
	ID                 string   `json:"id"`
	RequirementID      string   `json:"requirement_id"`
	GoalVersion        int      `json:"goal_version"`
	Title              string   `json:"title"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Status             Status   `json:"status"`
	LastRunID          string   `json:"last_run_id,omitempty"`
}

type Run struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	GoalVersion   int    `json:"goal_version"`
	Executor      string `json:"executor"`
	ActorID       string `json:"actor_id"`
	ContextID     string `json:"context_id,omitempty"`
	ContextHash   string `json:"context_hash,omitempty"`
	AdapterCursor string `json:"adapter_cursor,omitempty"`
	Status        Status `json:"status"`
	Result        string `json:"result,omitempty"`
}

type Evidence struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	RunID       string          `json:"run_id,omitempty"`
	ContextID   string          `json:"context_id,omitempty"`
	GoalVersion int             `json:"goal_version,omitempty"`
	Kind        string          `json:"kind"`
	URI         string          `json:"uri"`
	SHA256      string          `json:"sha256"`
	Outcome     string          `json:"outcome,omitempty"`
	Status      string          `json:"status,omitempty"`
	Command     string          `json:"command,omitempty"`
	Baseline    string          `json:"baseline,omitempty"`
	Source      string          `json:"source,omitempty"`
	Actor       Actor           `json:"actor,omitempty"`
	Checks      []EvidenceCheck `json:"checks,omitempty"`
}

type ContextSlice struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	GoalVersion  int             `json:"goal_version"`
	Revision     int             `json:"revision"`
	Summary      string          `json:"summary"`
	Sources      []ContextSource `json:"sources,omitempty"`
	AllowedPaths []string        `json:"allowed_paths,omitempty"`
	DeniedPaths  []string        `json:"denied_paths,omitempty"`
	SupersedesID string          `json:"supersedes_id,omitempty"`
	SliceHash    string          `json:"slice_hash"`
	Superseded   bool            `json:"superseded"`
}

type ContextSource struct {
	Kind     string   `json:"kind"`
	Ref      string   `json:"ref"`
	Digest   string   `json:"digest"`
	Reason   string   `json:"reason"`
	EventIDs []string `json:"event_ids,omitempty"`
	Excerpt  string   `json:"excerpt,omitempty"`
}

type ArtifactRef struct {
	Kind   string `json:"kind"`
	URI    string `json:"uri"`
	SHA256 string `json:"sha256"`
}

type Step struct {
	ID           string        `json:"id"`
	RunID        string        `json:"run_id"`
	Kind         string        `json:"kind"`
	Status       string        `json:"status"`
	Summary      string        `json:"summary"`
	ArtifactRefs []ArtifactRef `json:"artifact_refs,omitempty"`
}

type Checkpoint struct {
	ID              string `json:"id"`
	RunID           string `json:"run_id"`
	StepID          string `json:"step_id"`
	ContextHash     string `json:"context_hash"`
	WorkspaceDigest string `json:"workspace_digest"`
	AdapterCursor   string `json:"adapter_cursor"`
}

type ExecutorEvent struct {
	RunID         string        `json:"run_id"`
	StepID        string        `json:"step_id"`
	Kind          string        `json:"kind"`
	Cursor        string        `json:"cursor"`
	SourceEventID string        `json:"source_event_id,omitempty"`
	AdapterCursor string        `json:"adapter_cursor,omitempty"`
	Summary       string        `json:"summary"`
	Artifacts     []ArtifactRef `json:"artifacts,omitempty"`
}

type EvidenceCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type FileChange struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	SHA256     string `json:"sha256"`
	Baseline   string `json:"baseline"`
	Attributed bool   `json:"attributed"`
}

type ChangeAttribution struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	TaskID string `json:"task_id"`
	Note   string `json:"note,omitempty"`
}

type ProjectState struct {
	ProjectID          string                               `json:"project_id"`
	Goal               GoalVersion                          `json:"goal"`
	GoalChanges        map[string]GoalChange                `json:"goal_changes"`
	Leases             map[string]Lease                     `json:"leases"`
	Conflicts          map[string]Conflict                  `json:"conflicts"`
	Requirements       map[string]Requirement               `json:"requirements"`
	Tasks              map[string]Task                      `json:"tasks"`
	Runs               map[string]Run                       `json:"runs"`
	Evidence           map[string][]Evidence                `json:"evidence"`
	Contexts           map[string]ContextSlice              `json:"contexts"`
	Steps              map[string]Step                      `json:"steps"`
	Checkpoints        map[string]Checkpoint                `json:"checkpoints"`
	ExecutorEvents     map[string]ExecutorEvent             `json:"executor_events"`
	Changes            map[string]FileChange                `json:"changes"`
	Attributions       map[string]ChangeAttribution         `json:"attributions"`
	Agents             map[string]LogicalAgent              `json:"agents"`
	RuntimeBindings    map[string][]RuntimeBinding          `json:"runtime_bindings"`
	Missions           map[string]MissionEnvelope           `json:"missions"`
	Approvals          map[string]ApprovalRequest           `json:"approvals"`
	SCMRepositories    map[string]SCMRepository             `json:"scm_repositories"`
	CommitObservations map[string]CommitObservation         `json:"commit_observations"`
	SCMCommitStatus    map[string]string                    `json:"scm_commit_status"`
	SCMBindings        map[string]SCMBinding                `json:"scm_bindings"`
	SCMRemotes         map[string]SCMRemote                 `json:"scm_remotes"`
	SCMRemoteRefs      map[string]SCMRemoteRefObservation   `json:"scm_remote_refs"`
	SCMPullRequests    map[string]SCMPullRequestObservation `json:"scm_pull_requests"`
	SCMReviews         map[string]SCMReviewObservation      `json:"scm_reviews"`
	SCMChecks          map[string]SCMCheckObservation       `json:"scm_checks"`
}

type ProjectInitialized struct {
	Name string      `json:"name"`
	Goal GoalVersion `json:"goal"`
}

type GoalChangeProposed struct {
	GoalChange GoalChange `json:"goal_change"`
}

type GoalChangeApproved struct {
	GoalChangeID string    `json:"goal_change_id"`
	DeciderID    string    `json:"decider_id"`
	DecidedAt    time.Time `json:"decided_at"`
}

type GoalChangeRejected struct {
	GoalChangeID string    `json:"goal_change_id"`
	DeciderID    string    `json:"decider_id"`
	Reason       string    `json:"reason"`
	DecidedAt    time.Time `json:"decided_at"`
}

type LeaseIssued struct {
	Lease Lease `json:"lease"`
}

type LeaseRenewed struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type LeaseReleased struct {
	LeaseID string `json:"lease_id"`
}

type LeaseRevoked struct {
	LeaseID string `json:"lease_id"`
}

type ConflictOpened struct {
	Conflict Conflict `json:"conflict"`
}

type ConflictResolved struct {
	ConflictID string    `json:"conflict_id"`
	ResolverID string    `json:"resolver_id"`
	Resolution string    `json:"resolution"`
	ResolvedAt time.Time `json:"resolved_at"`
}

type RequirementPlanned struct {
	Requirement Requirement `json:"requirement"`
	Tasks       []Task      `json:"tasks"`
}

type RequirementApproved struct {
	RequirementID string `json:"requirement_id"`
}

type RunStarted struct {
	Run Run `json:"run"`
}

type RunFinished struct {
	RunID  string `json:"run_id"`
	Result string `json:"result"`
}

type RunResumed struct {
	RunID         string `json:"run_id"`
	AdapterCursor string `json:"adapter_cursor"`
	Reason        string `json:"reason"`
}

type EvidenceRecorded struct {
	Evidence Evidence `json:"evidence"`
}

type ContextIssued struct {
	Context ContextSlice `json:"context"`
}

type ContextSuperseded struct {
	ContextID string `json:"context_id"`
}

type RunStepStarted struct {
	Step Step `json:"step"`
}

type RunStepFinished struct {
	Step Step `json:"step"`
}

type CheckpointCreated struct {
	Checkpoint Checkpoint `json:"checkpoint"`
}

type ExecutorEventReceived struct {
	ExecutorEvent ExecutorEvent `json:"executor_event"`
}

type EvidenceCandidateRecorded struct {
	Evidence Evidence `json:"evidence"`
}

type EvidenceVerified struct {
	Evidence Evidence `json:"evidence"`
}

type EvidenceInvalidated struct {
	Evidence Evidence `json:"evidence"`
}

type TaskCompleted struct {
	TaskID string `json:"task_id"`
}

type ChangesScanned struct {
	Changes []FileChange `json:"changes"`
}

type ChangeAttributed struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	TaskID string `json:"task_id"`
	Note   string `json:"note,omitempty"`
}

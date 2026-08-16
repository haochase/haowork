// Package trace records immutable execution observations separately from governance facts.
package trace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/haochase/haowork/internal/model"
)

var (
	ErrHistoryCorrupt          = errors.New("trace history is corrupt")
	ErrSourceDivergent         = errors.New("trace source event diverges from stored history")
	ErrCursorRollback          = errors.New("trace cursor rolls back")
	ErrCursorGap               = errors.New("trace cursor has a gap")
	ErrTraceIDDivergent        = errors.New("trace id diverges from stored history")
	ErrMatrixAppendUnsupported = errors.New("trace store does not support atomic Matrix append")
	ErrStoreBusy               = errors.New("trace store is busy")
)

// Envelope is an immutable execution observation. Sequence and hashes are assigned by Store.
type Envelope struct {
	Sequence               uint64                `json:"sequence"`
	ID                     string                `json:"id"`
	InvocationID           string                `json:"invocation_id"`
	MissionID              string                `json:"mission_id"`
	GovernanceTaskID       string                `json:"governance_task_id"`
	WorkItemID             string                `json:"work_item_id"`
	RunID                  string                `json:"run_id"`
	LogicalActorID         string                `json:"logical_actor_id"`
	RuntimeBindingRevision int                   `json:"runtime_binding_revision"`
	AgentFunction          model.AgentFunction   `json:"agent_function,omitempty"`
	EnvironmentID          string                `json:"environment_id"`
	AgentTeamsInstanceID   string                `json:"agentteams_instance_id"`
	RoomID                 string                `json:"room_id,omitempty"`
	SenderID               string                `json:"sender_id,omitempty"`
	SourceEventID          string                `json:"source_event_id"`
	SourceEventType        string                `json:"source_event_type"`
	SourceSystem           string                `json:"source_system,omitempty"`
	ParentTraceID          string                `json:"parent_trace_id,omitempty"`
	Cursor                 string                `json:"cursor,omitempty"`
	ObservationSequence    uint64                `json:"observation_sequence,omitempty"`
	SkillName              string                `json:"skill_name,omitempty"`
	SkillVersion           string                `json:"skill_version,omitempty"`
	InputSHA256            string                `json:"input_sha256,omitempty"`
	OutputSHA256           string                `json:"output_sha256,omitempty"`
	ArtifactRefs           []model.ArtifactRef   `json:"artifact_refs,omitempty"`
	Artifacts              []ArtifactObservation `json:"artifacts,omitempty"`
	SummarySHA256          string                `json:"summary_sha256,omitempty"`
	Status                 string                `json:"status"`
	ErrorCode              string                `json:"error_code,omitempty"`
	StartedAt              time.Time             `json:"started_at"`
	FinishedAt             time.Time             `json:"finished_at,omitempty"`
	PreviousHash           string                `json:"previous_hash,omitempty"`
	Hash                   string                `json:"hash"`
}

// ArtifactObservation preserves the validated external metadata which ArtifactRef cannot represent.
type ArtifactObservation struct {
	Kind          string `json:"kind"`
	URI           string `json:"uri"`
	SHA256        string `json:"sha256"`
	EnvironmentID string `json:"environment_id"`
	Size          int64  `json:"size"`
}

type Store interface {
	AppendIdempotent(context.Context, Envelope) (Envelope, error)
	ReadAll(context.Context) ([]Envelope, error)
	Since(context.Context, uint64) ([]Envelope, error)
}

type CandidateKind string

const (
	CandidateCheckpoint CandidateKind = "checkpoint"
	CandidateEvidence   CandidateKind = "evidence"
	CandidateApproval   CandidateKind = "approval"
)

// PromotionCandidate must be re-authorized by app.Service or Team Core before a governance write.
type PromotionCandidate struct {
	Kind          CandidateKind   `json:"kind"`
	TraceID       string          `json:"trace_id"`
	PayloadSHA256 string          `json:"payload_sha256"`
	Payload       json.RawMessage `json:"payload"`
}

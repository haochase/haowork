// Package executionreceipt stores assignments and candidate execution receipts.
package executionreceipt

import (
	"context"
	"errors"
	"time"
)

var (
	ErrStoreUnavailable   = errors.New("execution receipt store is unavailable")
	ErrStoreBusy          = errors.New("execution receipt store is busy")
	ErrUnsafeStorePath    = errors.New("unsafe execution receipt store path")
	ErrHistoryCorrupt     = errors.New("execution receipt history is corrupt")
	ErrAssignmentNotFound = errors.New("execution assignment is not found")
	ErrAssignmentConflict = errors.New("execution assignment conflicts with stored history")
	ErrAssignmentExpired  = errors.New("execution assignment is expired")
	ErrAssignmentTerminal = errors.New("execution assignment is terminal")
	ErrInvalidTransition  = errors.New("execution assignment state transition is invalid")
	ErrReceiptNotFound    = errors.New("execution receipt is not found")
	ErrReceiptConflict    = errors.New("execution receipt conflicts with stored history")
)

type AssignmentState string

const (
	AssignmentPrepared        AssignmentState = "PREPARED"
	AssignmentDispatched      AssignmentState = "DISPATCHED"
	AssignmentReportSubmitted AssignmentState = "REPORT_SUBMITTED"
	AssignmentVerified        AssignmentState = "VERIFIED"
	AssignmentRejected        AssignmentState = "REJECTED"
	AssignmentExpired         AssignmentState = "EXPIRED"
	AssignmentDispatchFailed  AssignmentState = "DISPATCH_FAILED"
)

type ExecutionAssignment struct {
	ID                       string          `json:"id"`
	ProjectID                string          `json:"project_id"`
	MissionID                string          `json:"mission_id"`
	GovernanceTaskID         string          `json:"governance_task_id"`
	RunID                    string          `json:"run_id"`
	WorkItemID               string          `json:"work_item_id"`
	EnvironmentID            string          `json:"environment_id"`
	AgentTeamsInstanceID     string          `json:"agentteams_instance_id"`
	LeaderLogicalActorID     string          `json:"leader_logical_actor_id"`
	LeaderRuntimePrincipalID string          `json:"leader_runtime_principal_id"`
	AssignmentRevision       int             `json:"assignment_revision"`
	RuntimeBindingRevision   int             `json:"runtime_binding_revision"`
	GoalVersion              int             `json:"goal_version"`
	ContextID                string          `json:"context_id"`
	ContextHash              string          `json:"context_hash"`
	LeaseID                  string          `json:"lease_id"`
	MissionArtifactURI       string          `json:"mission_artifact_uri"`
	MissionArtifactSHA256    string          `json:"mission_artifact_sha256"`
	MatrixRoomID             string          `json:"matrix_room_id"`
	MatrixDispatchEventID    string          `json:"matrix_dispatch_event_id,omitempty"`
	State                    AssignmentState `json:"state"`
	CreatedAt                time.Time       `json:"created_at"`
	Deadline                 time.Time       `json:"deadline"`
}

type Artifact struct {
	Kind          string `json:"kind"`
	URI           string `json:"uri"`
	SHA256        string `json:"sha256"`
	EnvironmentID string `json:"environment_id"`
	Size          int64  `json:"size"`
}

type Check struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

type ExecutionReport struct {
	AssignmentID  string     `json:"assignment_id"`
	Status        string     `json:"status"`
	SummarySHA256 string     `json:"summary_sha256"`
	Artifacts     []Artifact `json:"artifacts"`
	Checks        []Check    `json:"checks"`
}

type ExecutionReceipt struct {
	ID           string          `json:"id"`
	AssignmentID string          `json:"assignment_id"`
	ReportSHA256 string          `json:"report_sha256"`
	Report       ExecutionReport `json:"report"`
	SubmittedAt  time.Time       `json:"submitted_at"`
}

// ValidAssignmentID recognizes the deterministic ID emitted by the
// AgentTeams transport: ASN- followed by the lowercase hex of 16 digest bytes.
func ValidAssignmentID(value string) bool {
	if len(value) != len("ASN-")+32 || value[:len("ASN-")] != "ASN-" {
		return false
	}
	for _, character := range value[len("ASN-"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// ValidReceiptID recognizes only receipt IDs derived from a SHA-256 digest.
func ValidReceiptID(value string) bool {
	if len(value) != len("ERC-")+64 || value[:len("ERC-")] != "ERC-" {
		return false
	}
	for _, character := range value[len("ERC-"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

type Store interface {
	Register(context.Context, ExecutionAssignment) (ExecutionAssignment, error)
	MarkDispatched(context.Context, string, string) (ExecutionAssignment, error)
	Submit(context.Context, ExecutionReceipt) (ExecutionReceipt, error)
	Assignment(context.Context, string) (ExecutionAssignment, error)
	Receipt(context.Context, string) (ExecutionReceipt, error)
	Wait(context.Context, string, time.Duration) (ExecutionReceipt, error)
}

// Package skillruntime loads canonical skill definitions and governs their invocation.
package skillruntime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/haochase/haowork/internal/model"
)

type RiskLevel string

const (
	RiskL0 RiskLevel = "L0"
	RiskL1 RiskLevel = "L1"
	RiskL2 RiskLevel = "L2"
	RiskL3 RiskLevel = "L3"
)

type Definition struct {
	Name                  string                `json:"name"`
	Version               string                `json:"version"`
	InputSchema           json.RawMessage       `json:"input_schema"`
	OutputSchema          json.RawMessage       `json:"output_schema"`
	Risk                  RiskLevel             `json:"risk"`
	AllowedFunctions      []model.AgentFunction `json:"allowed_functions"`
	RequiredContext       bool                  `json:"required_context"`
	RequiredLease         bool                  `json:"required_lease"`
	SupportedEnvironments []string              `json:"supported_environments"`
	Adapter               string                `json:"adapter"`
	Timeout               string                `json:"timeout"`
	RetryPolicy           string                `json:"retry_policy"`
	EvidencePolicy        string                `json:"evidence_policy"`
}

type Invocation struct {
	ID                     string          `json:"id"`
	MissionID              string          `json:"mission_id"`
	TaskID                 string          `json:"task_id"`
	WorkItemID             string          `json:"work_item_id"`
	RunID                  string          `json:"run_id"`
	LogicalActorID         string          `json:"logical_actor_id"`
	RuntimePrincipalID     string          `json:"runtime_principal_id"`
	AgentTeamsInstanceID   string          `json:"agentteams_instance_id"`
	RuntimeBindingRevision int             `json:"runtime_binding_revision"`
	SkillName              string          `json:"skill_name"`
	SkillVersion           string          `json:"skill_version"`
	EnvironmentID          string          `json:"environment_id"`
	TraceID                string          `json:"trace_id"`
	GoalVersion            int             `json:"goal_version"`
	ContextID              string          `json:"context_id"`
	ContextHash            string          `json:"context_hash"`
	LeaseID                string          `json:"lease_id"`
	Scope                  []string        `json:"scope"`
	Input                  json.RawMessage `json:"input"`
	InputSHA256            string          `json:"input_sha256"`
}

type Decision struct {
	Status        string              `json:"status"`
	Code          string              `json:"code"`
	ApprovalID    string              `json:"approval_id,omitempty"`
	AgentFunction model.AgentFunction `json:"agent_function,omitempty"`
}

const (
	DecisionAllow            = "Allow"
	DecisionDenied           = "Denied"
	DecisionApprovalRequired = "ApprovalRequired"

	CodeApprovalRequired   = "approval_required"
	CodeFunctionDenied     = "function_denied"
	CodeEnvironmentDenied  = "environment_denied"
	CodeScopeDenied        = "scope_denied"
	CodeInputHashMismatch  = "input_hash_mismatch"
	CodeAdapterFailed      = "adapter_failed"
	CodeAuditFailed        = "audit_failed"
	CodePolicyDenied       = "policy_denied"
	CodePolicyFailed       = "policy_failed"
	CodeDefinitionMismatch = "definition_mismatch"
	CodeLeaseMismatch      = "lease_mismatch"
	CodeTraceFailed        = "trace_failed"

	ResultSucceeded = "succeeded"
	ResultFailed    = "failed"
	ResultRejected  = "rejected"
)

type Result struct {
	InvocationID string              `json:"invocation_id"`
	Status       string              `json:"status"`
	ErrorCode    string              `json:"error_code,omitempty"`
	OutputSHA256 string              `json:"output_sha256,omitempty"`
	Output       json.RawMessage     `json:"output,omitempty"`
	Artifacts    []model.ArtifactRef `json:"artifacts,omitempty"`
}

type Adapter interface {
	Invoke(context.Context, Invocation) (json.RawMessage, []model.ArtifactRef, error)
}

type AuditSink interface {
	RecordSkillCall(context.Context, Invocation, Result) error
}

// ExecutionTracer records lifecycle observations outside the Governance Ledger.
// Implementations must fail closed when their append-only store is unavailable.
type ExecutionTracer interface {
	PolicyDecision(context.Context, Invocation, Decision) error
	ApprovalWait(context.Context, Invocation, Decision) error
	AdapterStarted(context.Context, Invocation, Decision) error
	AdapterFinished(context.Context, Invocation, Decision, Result) error
	AuditResult(context.Context, Invocation, Decision, Result, error) error
	Promote(context.Context, Invocation, Decision, Result) error
}

type StateReader interface {
	State(context.Context) (model.ProjectState, error)
}

type StateReaderFunc func(context.Context) (model.ProjectState, error)

func (fn StateReaderFunc) State(ctx context.Context) (model.ProjectState, error) {
	return fn(ctx)
}

func StaticState(state model.ProjectState) StateReader {
	return StateReaderFunc(func(context.Context) (model.ProjectState, error) {
		return state, nil
	})
}

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (fn ClockFunc) Now() time.Time {
	return fn()
}

package executionreceipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

const (
	maxReportArtifacts = 32
	maxReportChecks    = 32
	maxArtifactBytes   = 16 * 1024 * 1024
)

var (
	ErrAssignmentInactive      = errors.New("execution assignment is inactive")
	ErrPrincipalMismatch       = errors.New("execution receipt principal does not match assignment")
	ErrBindingRevisionMismatch = errors.New("execution receipt binding revision does not match assignment")
	ErrLeaseInvalid            = errors.New("execution assignment lease is invalid")
	ErrReceiptSchemaInvalid    = errors.New("execution receipt schema is invalid")
	ErrReceiptStoreFailed      = errors.New("execution receipt cannot be reliably stored")
)

// Principal is the server-authenticated runtime identity. Caller-provided report fields never populate it.
type Principal struct {
	LogicalActorID       string
	RuntimePrincipalID   string
	EnvironmentID        string
	AgentTeamsInstanceID string
	BindingRevision      int
	Function             model.AgentFunction
}

// Service validates and records candidate reports without promoting governance state.
type Service struct {
	Store Store
	Trace trace.Store
	Clock func() time.Time
}

func (service Service) Submit(ctx context.Context, principal Principal, report ExecutionReport) (ExecutionReceipt, error) {
	if service.Store == nil || service.Trace == nil || service.Clock == nil {
		return ExecutionReceipt{}, ErrReceiptStoreFailed
	}
	if err := ctx.Err(); err != nil {
		return ExecutionReceipt{}, err
	}
	assignment, err := service.Store.Assignment(ctx, strings.TrimSpace(report.AssignmentID))
	if err != nil {
		if errors.Is(err, ErrAssignmentNotFound) {
			return ExecutionReceipt{}, ErrAssignmentNotFound
		}
		return ExecutionReceipt{}, ErrReceiptStoreFailed
	}
	if err := validatePrincipal(assignment, principal); err != nil {
		return ExecutionReceipt{}, err
	}
	now := service.Clock().UTC()
	if now.IsZero() {
		return ExecutionReceipt{}, ErrReceiptStoreFailed
	}
	if err := validateActiveAssignment(assignment, now); err != nil {
		return ExecutionReceipt{}, err
	}
	canonical, reportDigest, err := canonicalReport(report)
	if err != nil || validateReport(assignment, canonical) != nil {
		return ExecutionReceipt{}, ErrReceiptSchemaInvalid
	}
	receipt := ExecutionReceipt{
		ID:           receiptID(assignment, reportDigest),
		AssignmentID: assignment.ID,
		ReportSHA256: reportDigest,
		Report:       canonical,
		SubmittedAt:  now,
	}
	stored, err := service.Store.Submit(ctx, receipt)
	if err != nil {
		switch {
		case errors.Is(err, ErrReceiptConflict):
			return ExecutionReceipt{}, ErrReceiptConflict
		case errors.Is(err, ErrAssignmentNotFound):
			return ExecutionReceipt{}, ErrAssignmentNotFound
		case errors.Is(err, ErrAssignmentExpired), errors.Is(err, ErrAssignmentTerminal), errors.Is(err, ErrInvalidTransition):
			return ExecutionReceipt{}, ErrAssignmentInactive
		default:
			return ExecutionReceipt{}, ErrReceiptStoreFailed
		}
	}
	if err := appendCandidateTrace(ctx, service.Trace, assignment, stored); err != nil {
		// The receipt remains a non-promoted candidate. Retrying the same payload
		// reuses it and repairs the idempotent trace append.
		return ExecutionReceipt{}, ErrReceiptStoreFailed
	}
	return stored, nil
}

func validatePrincipal(assignment ExecutionAssignment, principal Principal) error {
	if principal.BindingRevision != assignment.RuntimeBindingRevision {
		return ErrBindingRevisionMismatch
	}
	if principal.Function != model.FunctionDeliveryLeader ||
		strings.TrimSpace(principal.LogicalActorID) != assignment.LeaderLogicalActorID ||
		strings.TrimSpace(principal.RuntimePrincipalID) != assignment.LeaderRuntimePrincipalID ||
		strings.TrimSpace(principal.EnvironmentID) != assignment.EnvironmentID ||
		strings.TrimSpace(principal.AgentTeamsInstanceID) != assignment.AgentTeamsInstanceID {
		return ErrPrincipalMismatch
	}
	return nil
}

func validateActiveAssignment(assignment ExecutionAssignment, now time.Time) error {
	if strings.TrimSpace(assignment.LeaseID) == "" {
		return ErrLeaseInvalid
	}
	if assignment.Deadline.IsZero() {
		return ErrAssignmentInactive
	}
	switch assignment.State {
	case AssignmentDispatched:
		if !assignment.Deadline.After(now) {
			return ErrAssignmentInactive
		}
		return nil
	case AssignmentReportSubmitted:
		return nil
	default:
		return ErrAssignmentInactive
	}
}

func validateReport(assignment ExecutionAssignment, report ExecutionReport) error {
	if report.AssignmentID != assignment.ID || !validReportStatus(report.Status) || !validSHA256(report.SummarySHA256) {
		return ErrReceiptSchemaInvalid
	}
	if len(report.Artifacts) > maxReportArtifacts || len(report.Checks) > maxReportChecks {
		return ErrReceiptSchemaInvalid
	}
	missionLocation, err := canonicalS3ArtifactLocation(assignment.MissionArtifactURI, "", assignment.EnvironmentID, assignment.MissionID, assignment.MissionArtifactSHA256)
	if err != nil {
		return ErrReceiptSchemaInvalid
	}
	for _, artifact := range report.Artifacts {
		if artifact.Kind == "" || artifact.EnvironmentID != assignment.EnvironmentID || !validSHA256(artifact.SHA256) || artifact.Size < 0 || artifact.Size > maxArtifactBytes {
			return ErrReceiptSchemaInvalid
		}
		if _, err := canonicalS3ArtifactLocation(artifact.URI, missionLocation.Host, assignment.EnvironmentID, assignment.MissionID, artifact.SHA256); err != nil {
			return ErrReceiptSchemaInvalid
		}
	}
	for _, check := range report.Checks {
		if check.Name == "" || !validCheckStatus(check.Status) || !validSHA256(check.EvidenceSHA256) {
			return ErrReceiptSchemaInvalid
		}
	}
	return nil
}

func canonicalS3ArtifactLocation(raw, bucket, environmentID, missionID, digest string) (*url.URL, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.Contains(raw, "%") || !validSHA256(digest) || !validArtifactScopeSegment(environmentID) || !validArtifactScopeSegment(missionID) {
		return nil, ErrReceiptSchemaInvalid
	}
	location, err := url.Parse(raw)
	if err != nil || location.Scheme != "s3" || location.Host == "" || location.User != nil || location.Opaque != "" || location.RawPath != "" || location.RawQuery != "" || location.Fragment != "" {
		return nil, ErrReceiptSchemaInvalid
	}
	if bucket != "" && location.Host != bucket {
		return nil, ErrReceiptSchemaInvalid
	}
	wantPath := "/environments/" + environmentID + "/missions/" + missionID + "/artifacts/" + digest
	if location.Path != wantPath {
		return nil, ErrReceiptSchemaInvalid
	}
	canonical := (&url.URL{Scheme: "s3", Host: location.Host, Path: wantPath}).String()
	if canonical != raw {
		return nil, ErrReceiptSchemaInvalid
	}
	return location, nil
}

func validArtifactScopeSegment(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && value != "." && value != ".." && url.PathEscape(value) == value
}

func validReportStatus(value string) bool {
	switch value {
	case "succeeded", "failed", "blocked":
		return true
	default:
		return false
	}
}

func validCheckStatus(value string) bool {
	switch value {
	case "passed", "failed", "blocked":
		return true
	default:
		return false
	}
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func receiptID(assignment ExecutionAssignment, reportDigest string) string {
	bound := struct {
		AssignmentID             string `json:"assignment_id"`
		AssignmentRevision       int    `json:"assignment_revision"`
		MissionID                string `json:"mission_id"`
		RunID                    string `json:"run_id"`
		WorkItemID               string `json:"work_item_id"`
		LeaderRuntimePrincipalID string `json:"leader_runtime_principal_id"`
		RuntimeBindingRevision   int    `json:"runtime_binding_revision"`
		EnvironmentID            string `json:"environment_id"`
		AgentTeamsInstanceID     string `json:"agentteams_instance_id"`
		CanonicalReportSHA256    string `json:"canonical_report_sha256"`
	}{
		AssignmentID:             assignment.ID,
		AssignmentRevision:       assignment.AssignmentRevision,
		MissionID:                assignment.MissionID,
		RunID:                    assignment.RunID,
		WorkItemID:               assignment.WorkItemID,
		LeaderRuntimePrincipalID: assignment.LeaderRuntimePrincipalID,
		RuntimeBindingRevision:   assignment.RuntimeBindingRevision,
		EnvironmentID:            assignment.EnvironmentID,
		AgentTeamsInstanceID:     assignment.AgentTeamsInstanceID,
		CanonicalReportSHA256:    reportDigest,
	}
	encoded, _ := json.Marshal(bound)
	digest := sha256.Sum256(encoded)
	return "ERC-" + hex.EncodeToString(digest[:])
}

func appendCandidateTrace(ctx context.Context, store trace.Store, assignment ExecutionAssignment, receipt ExecutionReceipt) error {
	record := CandidateTraceEnvelope(assignment, receipt)
	_, err := store.AppendIdempotent(ctx, record)
	return err
}

// CandidateTraceEnvelope is the single canonical observation for a submitted
// execution report. MCP submission and bridge recovery must append this exact
// envelope so idempotent repair cannot create a second interpretation.
func CandidateTraceEnvelope(assignment ExecutionAssignment, receipt ExecutionReceipt) trace.Envelope {
	artifacts := make([]trace.ArtifactObservation, len(receipt.Report.Artifacts))
	for index, artifact := range receipt.Report.Artifacts {
		artifacts[index] = trace.ArtifactObservation{
			Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256,
			EnvironmentID: artifact.EnvironmentID, Size: artifact.Size,
		}
	}
	return trace.Envelope{
		ID:                     "TRC-" + strings.TrimPrefix(receipt.ID, "ERC-"),
		InvocationID:           assignment.ID,
		AssignmentID:           assignment.ID,
		AssignmentRevision:     assignment.AssignmentRevision,
		MissionID:              assignment.MissionID,
		GovernanceTaskID:       assignment.GovernanceTaskID,
		WorkItemID:             assignment.WorkItemID,
		RunID:                  assignment.RunID,
		GoalVersion:            assignment.GoalVersion,
		ContextID:              assignment.ContextID,
		ContextHash:            assignment.ContextHash,
		LeaseID:                assignment.LeaseID,
		LogicalActorID:         assignment.LeaderLogicalActorID,
		RuntimeBindingRevision: assignment.RuntimeBindingRevision,
		AgentFunction:          model.FunctionDeliveryLeader,
		EnvironmentID:          assignment.EnvironmentID,
		AgentTeamsInstanceID:   assignment.AgentTeamsInstanceID,
		RoomID:                 assignment.MatrixRoomID,
		SourceEventID:          receipt.ID,
		SourceEventType:        "execution.report.submitted",
		SourceSystem:           "haowork-mcp",
		OutputSHA256:           receipt.ReportSHA256,
		ReportSHA256:           receipt.ReportSHA256,
		SummarySHA256:          receipt.Report.SummarySHA256,
		ReportStatus:           receipt.Report.Status,
		Candidate:              true,
		Artifacts:              artifacts,
		Status:                 "candidate",
		StartedAt:              receipt.SubmittedAt,
		FinishedAt:             receipt.SubmittedAt,
	}
}

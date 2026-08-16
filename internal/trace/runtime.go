package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

// CandidateSink sends a candidate back to an application or Team Core authorization boundary.
// It must idempotently accept the same TraceID and PayloadSHA256 after a delivery retry.
// It has no authority to persist governance events itself.
type CandidateSink interface {
	SubmitCandidate(context.Context, PromotionCandidate) error
}

// RuntimeTracer is the concrete skillruntime.ExecutionTracer backed by the trace JSONL store.
type RuntimeTracer struct {
	Store      Store
	Candidates CandidateSink
	Clock      skillruntime.Clock
}

func (tracer RuntimeTracer) PolicyDecision(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision) error {
	status := "denied"
	if decision.Status == skillruntime.DecisionAllow {
		status = "allowed"
	} else if decision.Status == skillruntime.DecisionApprovalRequired {
		status = "approval_required"
	}
	return tracer.append(ctx, invocation, decision, "skill.policy.decided", status, decision.Code, nil)
}

func (tracer RuntimeTracer) ApprovalWait(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision) error {
	payload, err := json.Marshal(struct {
		InvocationID, MissionID, TaskID, SkillName, InputSHA256, ApprovalID string
	}{invocation.ID, invocation.MissionID, invocation.TaskID, invocation.SkillName, invocation.InputSHA256, decision.ApprovalID})
	if err != nil {
		return err
	}
	record, err := tracer.record(ctx, invocation, decision, "approval.requested", "waiting", decision.Code, nil)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	record.SummarySHA256 = hex.EncodeToString(digest[:])
	return tracer.submitCandidate(ctx, record, payload)
}

func (tracer RuntimeTracer) AdapterStarted(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision) error {
	return tracer.append(ctx, invocation, decision, "skill.adapter.started", "running", "", nil)
}

func (tracer RuntimeTracer) AdapterFinished(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, result skillruntime.Result) error {
	return tracer.appendResult(ctx, invocation, decision, "skill.adapter.finished", result.Status, result.ErrorCode, result)
}

func (tracer RuntimeTracer) AuditResult(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, result skillruntime.Result, auditErr error) error {
	code := ""
	status := skillruntime.ResultSucceeded
	if auditErr != nil {
		status, code = skillruntime.ResultFailed, skillruntime.CodeAuditFailed
	}
	return tracer.appendResult(ctx, invocation, decision, "skill.audit.recorded", status, code, result)
}

func (tracer RuntimeTracer) Promote(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, result skillruntime.Result) error {
	if invocation.SkillName != "audit" {
		return nil
	}
	if result.Status != skillruntime.ResultSucceeded || result.OutputSHA256 == "" {
		return errors.New("successful audited result is required before promotion")
	}
	record, err := tracer.record(ctx, invocation, decision, "evidence.candidate", skillruntime.ResultSucceeded, "", result.Artifacts)
	if err != nil {
		return err
	}
	record.OutputSHA256 = result.OutputSHA256
	return tracer.submitCandidate(ctx, record, result.Output)
}

func (tracer RuntimeTracer) submitCandidate(ctx context.Context, record Envelope, payload json.RawMessage) error {
	if tracer.Store == nil || tracer.Candidates == nil {
		return errors.New("trace store and promotion authorization sink are required")
	}
	store, ok := tracer.Store.(interface {
		AppendIdempotentResult(context.Context, Envelope) (Envelope, bool, error)
	})
	if !ok {
		return errors.New("trace store does not support idempotent candidate delivery")
	}
	stored, _, err := store.AppendIdempotentResult(ctx, record)
	if err != nil {
		return err
	}
	candidate, err := Promote(stored, payload)
	if err != nil {
		return err
	}
	return tracer.Candidates.SubmitCandidate(ctx, candidate)
}

func (tracer RuntimeTracer) append(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, eventType, status, errorCode string, artifacts []model.ArtifactRef) error {
	if tracer.Store == nil {
		return errors.New("trace store is required")
	}
	record, err := tracer.record(ctx, invocation, decision, eventType, status, errorCode, artifacts)
	if err != nil {
		return err
	}
	_, err = tracer.Store.AppendIdempotent(ctx, record)
	return err
}

func (tracer RuntimeTracer) appendResult(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, eventType, status, errorCode string, result skillruntime.Result) error {
	if tracer.Store == nil {
		return errors.New("trace store is required")
	}
	record, err := tracer.record(ctx, invocation, decision, eventType, status, errorCode, result.Artifacts)
	if err != nil {
		return err
	}
	record.OutputSHA256 = result.OutputSHA256
	_, err = tracer.Store.AppendIdempotent(ctx, record)
	return err
}

func (tracer RuntimeTracer) record(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision, eventType, status, errorCode string, artifacts []model.ArtifactRef) (Envelope, error) {
	root, err := tracer.ensureRoot(ctx, invocation, decision)
	if err != nil {
		return Envelope{}, err
	}
	return tracer.recordAt(invocation, decision, eventType, status, errorCode, artifacts, root.StartedAt), nil
}

func (tracer RuntimeTracer) ensureRoot(ctx context.Context, invocation skillruntime.Invocation, decision skillruntime.Decision) (Envelope, error) {
	if tracer.Store == nil {
		return Envelope{}, errors.New("trace store is required")
	}
	store, ok := tracer.Store.(interface {
		AppendInvocationRoot(context.Context, Envelope) (Envelope, error)
	})
	if !ok {
		return Envelope{}, errors.New("trace store does not support invocation roots")
	}
	now := time.Now().UTC()
	if tracer.Clock != nil {
		now = tracer.Clock.Now().UTC()
	}
	root := tracer.recordAt(invocation, decision, "trace.invocation.started", "started", "", nil, now)
	root.ID, root.ParentTraceID = invocation.TraceID, ""
	root.SourceEventID = invocation.ID + ":trace.invocation.started"
	return store.AppendInvocationRoot(ctx, root)
}

func (tracer RuntimeTracer) recordAt(invocation skillruntime.Invocation, decision skillruntime.Decision, eventType, status, errorCode string, artifacts []model.ArtifactRef, now time.Time) Envelope {
	return Envelope{
		ID: invocation.TraceID + ":" + invocation.ID + ":" + eventType, InvocationID: invocation.ID, MissionID: invocation.MissionID, GovernanceTaskID: invocation.TaskID, WorkItemID: invocation.WorkItemID, RunID: invocation.RunID,
		LogicalActorID: invocation.LogicalActorID, RuntimeBindingRevision: invocation.RuntimeBindingRevision, AgentFunction: decision.AgentFunction,
		EnvironmentID: invocation.EnvironmentID, AgentTeamsInstanceID: invocation.AgentTeamsInstanceID,
		SourceEventID: invocation.ID + ":" + eventType, SourceEventType: eventType, ParentTraceID: invocation.TraceID,
		SkillName: invocation.SkillName, SkillVersion: invocation.SkillVersion, InputSHA256: invocation.InputSHA256, OutputSHA256: "",
		ArtifactRefs: append([]model.ArtifactRef(nil), artifacts...), Status: status, ErrorCode: errorCode, StartedAt: now, FinishedAt: now,
	}
}

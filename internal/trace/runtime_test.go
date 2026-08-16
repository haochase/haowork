package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

func TestRuntimeTracePromotesAuditOnlyAfterAuditSinkSucceeds(t *testing.T) {
	invocation, state := traceRuntimeFixture(t)
	registry, err := skillruntime.Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidates := &candidateRecorder{}
	runtime := skillruntime.Runtime{
		Policy:  skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)},
		Adapter: runtimeAdapter{output: json.RawMessage(`{"audit_id":"AUD-001"}`)},
		Audit:   acceptingAudit{},
		Tracer:  RuntimeTracer{Store: store, Candidates: candidates, Clock: fixedTraceClock{}},
	}

	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != skillruntime.ResultSucceeded {
		t.Fatalf("result = %#v, want succeeded", result)
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(records))
	for _, record := range records {
		got = append(got, record.SourceEventType)
	}
	want := []string{"trace.invocation.started", "skill.policy.decided", "skill.adapter.started", "skill.adapter.finished", "skill.audit.recorded", "evidence.candidate"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trace event types = %#v, want %#v", got, want)
	}
	if len(candidates.values) != 1 || candidates.values[0].Kind != CandidateEvidence {
		t.Fatalf("promotion candidates = %#v, want one evidence candidate", candidates.values)
	}
}

func TestRuntimeTraceNeverPromotesAdapterOutputWhenAuditFails(t *testing.T) {
	invocation, state := traceRuntimeFixture(t)
	registry, err := skillruntime.Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidates := &candidateRecorder{}
	runtime := skillruntime.Runtime{
		Policy:  skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)},
		Adapter: runtimeAdapter{output: json.RawMessage(`{"audit_id":"AUD-001"}`)},
		Audit:   rejectingAudit{},
		Tracer:  RuntimeTracer{Store: store, Candidates: candidates, Clock: fixedTraceClock{}},
	}

	result, err := runtime.Invoke(context.Background(), invocation)
	if err == nil || result.Status != skillruntime.ResultFailed || result.ErrorCode != skillruntime.CodeAuditFailed {
		t.Fatalf("result/error = %#v / %v, want audit failure", result, err)
	}
	records, readErr := store.ReadAll(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(records) != 5 || len(candidates.values) != 0 {
		t.Fatalf("trace/candidates = %#v / %#v, adapter output must not promote before audit", records, candidates.values)
	}
}

func TestRuntimeTraceFailsClosedWhenPromotionAuthorizationIsUnavailable(t *testing.T) {
	invocation, state := traceRuntimeFixture(t)
	registry, err := skillruntime.Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := skillruntime.Runtime{
		Policy:  skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)},
		Adapter: runtimeAdapter{output: json.RawMessage(`{"audit_id":"AUD-001"}`)},
		Audit:   acceptingAudit{},
		Tracer:  RuntimeTracer{Store: NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock")), Candidates: rejectingCandidateSink{}, Clock: fixedTraceClock{}},
	}
	result, err := runtime.Invoke(context.Background(), invocation)
	if err == nil || result.Status != skillruntime.ResultFailed || result.ErrorCode != skillruntime.CodeTraceFailed {
		t.Fatalf("result/error = %#v / %v, want trace failure", result, err)
	}
}

func TestRuntimeTraceSubmitsApprovalCandidateOnceForIdempotentWait(t *testing.T) {
	invocation, state := approvalTraceRuntimeFixture(t)
	registry, err := skillruntime.Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidates := &candidateRecorder{}
	runtime := skillruntime.Runtime{Policy: skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)}, Adapter: runtimeAdapter{}, Audit: acceptingAudit{}, Tracer: RuntimeTracer{Store: store, Candidates: candidates, Clock: fixedTraceClock{}}}
	for attempt := 0; attempt < 2; attempt++ {
		result, invokeErr := runtime.Invoke(context.Background(), invocation)
		if invokeErr != nil || result.Status != skillruntime.ResultRejected || result.ErrorCode != skillruntime.CodeApprovalRequired {
			t.Fatalf("attempt %d result/error = %#v / %v, want approval wait", attempt, result, invokeErr)
		}
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[2].SourceEventType != "approval.requested" || records[2].Status != "waiting" {
		t.Fatalf("records = %#v, want policy plus waiting approval", records)
	}
	if len(candidates.values) != 1 || candidates.values[0].Kind != CandidateApproval {
		t.Fatalf("approval candidates = %#v, want exactly one CandidateApproval", candidates.values)
	}
}

func TestRuntimeTraceReusesRootTimestampForAdvancingClockRetries(t *testing.T) {
	invocation, _ := traceRuntimeFixture(t)
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	tracer := RuntimeTracer{
		Store: store,
		Clock: &advancingTraceClock{values: []time.Time{
			time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 8, 11, 10, 0, 1, 0, time.UTC),
		}},
	}
	decision := skillruntime.Decision{Status: skillruntime.DecisionAllow, AgentFunction: model.FunctionVerify}
	for attempt := 0; attempt < 2; attempt++ {
		if err := tracer.PolicyDecision(context.Background(), invocation, decision); err != nil {
			t.Fatalf("attempt %d PolicyDecision() error = %v", attempt, err)
		}
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != invocation.TraceID || records[1].ParentTraceID != invocation.TraceID || !records[1].StartedAt.Equal(records[0].StartedAt) {
		t.Fatalf("records = %#v, want idempotent root followed by child with root timestamp", records)
	}
	projection, err := Replay(records)
	if err != nil {
		t.Fatal(err)
	}
	if parent, ok := projection.Parent(records[1].ID); !ok || parent.ID != invocation.TraceID {
		t.Fatalf("Parent(%q) = %#v, %v; want root trace", records[1].ID, parent, ok)
	}
}

func TestRuntimeTraceReusesPersistedRootWithDefaultClock(t *testing.T) {
	invocation, _ := traceRuntimeFixture(t)
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	tracer := RuntimeTracer{Store: store}
	decision := skillruntime.Decision{Status: skillruntime.DecisionAllow, AgentFunction: model.FunctionVerify}
	for attempt := 0; attempt < 2; attempt++ {
		if err := tracer.PolicyDecision(context.Background(), invocation, decision); err != nil {
			t.Fatalf("attempt %d PolicyDecision() error = %v", attempt, err)
		}
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || !records[1].StartedAt.Equal(records[0].StartedAt) {
		t.Fatalf("records = %#v, want retry to reuse persisted root timestamp", records)
	}
}

func TestRuntimeTraceRetriesCandidateDeliveryAfterStoredCandidateSinkFailure(t *testing.T) {
	invocation, _ := approvalTraceRuntimeFixture(t)
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidates := &flakyCandidateSink{failures: 1}
	tracer := RuntimeTracer{Store: store, Candidates: candidates, Clock: fixedTraceClock{}}
	decision := skillruntime.Decision{Status: skillruntime.DecisionApprovalRequired, Code: skillruntime.CodeApprovalRequired, ApprovalID: invocation.InputSHA256, AgentFunction: model.FunctionBuild}
	if err := tracer.ApprovalWait(context.Background(), invocation, decision); err == nil {
		t.Fatal("first ApprovalWait() error = nil, want candidate delivery failure")
	}
	if err := tracer.ApprovalWait(context.Background(), invocation, decision); err != nil {
		t.Fatalf("retry ApprovalWait() error = %v", err)
	}
	if candidates.calls != 2 || len(candidates.values) != 1 || candidates.values[0].Kind != CandidateApproval {
		t.Fatalf("candidate calls/values = %d / %#v, want retry delivery", candidates.calls, candidates.values)
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].SourceEventType != "approval.requested" {
		t.Fatalf("records = %#v, want root plus one persisted approval candidate", records)
	}
}

func TestRuntimeTraceRetriesEvidenceCandidateDeliveryAfterStoredSinkFailure(t *testing.T) {
	invocation, _ := traceRuntimeFixture(t)
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	candidates := &flakyCandidateSink{failures: 1}
	tracer := RuntimeTracer{Store: store, Candidates: candidates, Clock: fixedTraceClock{}}
	result := skillruntime.Result{Status: skillruntime.ResultSucceeded, OutputSHA256: "audit-output", Output: json.RawMessage(`{"audit_id":"AUD-001"}`)}
	decision := skillruntime.Decision{Status: skillruntime.DecisionAllow, AgentFunction: model.FunctionVerify}
	if err := tracer.Promote(context.Background(), invocation, decision, result); err == nil {
		t.Fatal("first Promote() error = nil, want candidate delivery failure")
	}
	if err := tracer.Promote(context.Background(), invocation, decision, result); err != nil {
		t.Fatalf("retry Promote() error = %v", err)
	}
	if candidates.calls != 2 || len(candidates.values) != 1 || candidates.values[0].Kind != CandidateEvidence {
		t.Fatalf("candidate calls/values = %d / %#v, want retry evidence delivery", candidates.calls, candidates.values)
	}
}

func TestRuntimeTraceRecordsEarlyPolicyDenialWithoutAgentFunction(t *testing.T) {
	invocation, _ := traceRuntimeFixture(t)
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	tracer := RuntimeTracer{Store: store, Clock: fixedTraceClock{}}
	decision := skillruntime.Decision{Status: skillruntime.DecisionDenied, Code: "runtime_binding_mismatch"}
	if err := tracer.PolicyDecision(context.Background(), invocation, decision); err != nil {
		t.Fatalf("PolicyDecision() error = %v, want denial trace to append", err)
	}
	records, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Status != "denied" || records[1].AgentFunction != "" {
		t.Fatalf("records = %#v, want root plus unbound denied policy decision", records)
	}
}

func TestRuntimeTracePreservesEarlyPolicyDenialResult(t *testing.T) {
	invocation, _ := traceRuntimeFixture(t)
	invocation.InputSHA256 = "not-the-input-digest"
	store := NewAt(filepath.Join(t.TempDir(), "execution.jsonl"), filepath.Join(t.TempDir(), "execution.lock"))
	runtime := skillruntime.Runtime{Policy: skillruntime.Policy{}, Tracer: RuntimeTracer{Store: store, Clock: fixedTraceClock{}}}
	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil || result.Status != skillruntime.ResultRejected || result.ErrorCode != skillruntime.CodeInputHashMismatch {
		t.Fatalf("result/error = %#v / %v, want input-hash denial without trace failure", result, err)
	}
}

type runtimeAdapter struct{ output json.RawMessage }

func (adapter runtimeAdapter) Invoke(context.Context, skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	return adapter.output, []model.ArtifactRef{{Kind: "audit", URI: "artifact://audit-001", SHA256: "artifact-hash"}}, nil
}

type acceptingAudit struct{}

func (acceptingAudit) RecordSkillCall(context.Context, skillruntime.Invocation, skillruntime.Result) error {
	return nil
}

type rejectingAudit struct{}

func (rejectingAudit) RecordSkillCall(context.Context, skillruntime.Invocation, skillruntime.Result) error {
	return errors.New("audit unavailable")
}

type candidateRecorder struct {
	values []PromotionCandidate
	seen   map[string]struct{}
}

func (recorder *candidateRecorder) SubmitCandidate(_ context.Context, candidate PromotionCandidate) error {
	if recorder.seen == nil {
		recorder.seen = make(map[string]struct{})
	}
	key := candidate.TraceID + "\x00" + candidate.PayloadSHA256
	if _, exists := recorder.seen[key]; exists {
		return nil
	}
	recorder.seen[key] = struct{}{}
	recorder.values = append(recorder.values, candidate)
	return nil
}

type rejectingCandidateSink struct{}

func (rejectingCandidateSink) SubmitCandidate(context.Context, PromotionCandidate) error {
	return errors.New("authorization unavailable")
}

type fixedTraceClock struct{}

func (fixedTraceClock) Now() time.Time {
	return time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
}

type advancingTraceClock struct {
	values []time.Time
	index  int
}

func (clock *advancingTraceClock) Now() time.Time {
	value := clock.values[clock.index]
	if clock.index < len(clock.values)-1 {
		clock.index++
	}
	return value
}

type flakyCandidateSink struct {
	calls    int
	failures int
	values   []PromotionCandidate
}

func (sink *flakyCandidateSink) SubmitCandidate(_ context.Context, candidate PromotionCandidate) error {
	sink.calls++
	if sink.calls <= sink.failures {
		return errors.New("authorization temporarily unavailable")
	}
	sink.values = append(sink.values, candidate)
	return nil
}

func traceRuntimeFixture(t *testing.T) (skillruntime.Invocation, model.ProjectState) {
	t.Helper()
	input := json.RawMessage(`{"artifact_sha256":"artifact-hash"}`)
	digest := sha256.Sum256(input)
	invocation := skillruntime.Invocation{
		ID: "INV-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001",
		LogicalActorID: "AGT-VERIFY", RuntimePrincipalID: "runtime-verify", AgentTeamsInstanceID: "AT-001", RuntimeBindingRevision: 1,
		SkillName: "audit", SkillVersion: "1.0.0", EnvironmentID: "public", TraceID: "TRC-ROOT-001", GoalVersion: 7,
		ContextID: "CTX-001", ContextHash: "context-hash", LeaseID: "LSE-001", Scope: []string{"internal/trace"}, Input: input, InputSHA256: hex.EncodeToString(digest[:]),
	}
	state := model.ProjectState{
		Goal:            model.GoalVersion{Version: invocation.GoalVersion},
		Agents:          map[string]model.LogicalAgent{invocation.LogicalActorID: {ID: invocation.LogicalActorID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionVerify, Status: "active"}},
		RuntimeBindings: map[string][]model.RuntimeBinding{invocation.LogicalActorID: {{LogicalActorID: invocation.LogicalActorID, Revision: invocation.RuntimeBindingRevision, EnvironmentID: invocation.EnvironmentID, RuntimePrincipalID: invocation.RuntimePrincipalID, AgentTeamsInstanceID: invocation.AgentTeamsInstanceID, Status: "active"}}},
		Missions:        map[string]model.MissionEnvelope{invocation.MissionID: {ID: invocation.MissionID, GoalVersion: invocation.GoalVersion, ContextID: invocation.ContextID, ContextHash: invocation.ContextHash, LeaseID: invocation.LeaseID, AllowedScopes: []string{"internal/trace"}, AllowedSkills: []model.MissionSkillGrant{{Name: invocation.SkillName, Version: invocation.SkillVersion}}, EnvironmentID: invocation.EnvironmentID, RiskLevel: "L1"}},
		Contexts:        map[string]model.ContextSlice{invocation.ContextID: {ID: invocation.ContextID, TaskID: invocation.TaskID, GoalVersion: invocation.GoalVersion, SliceHash: invocation.ContextHash}},
		Leases:          map[string]model.Lease{invocation.LeaseID: {ID: invocation.LeaseID, TaskID: invocation.TaskID, SubjectKind: "logical_agent", SubjectID: invocation.LogicalActorID, EnvironmentID: invocation.EnvironmentID, ContextID: invocation.ContextID, GoalVersion: invocation.GoalVersion, AllowedScopes: []string{"internal/trace"}, AllowedSkills: []string{invocation.SkillName}, Status: "active", StartsAt: time.Now().UTC().Add(-time.Hour), ExpiresAt: time.Now().UTC().Add(time.Hour)}},
		Runs:            map[string]model.Run{invocation.RunID: {ID: invocation.RunID, TaskID: invocation.TaskID, GoalVersion: invocation.GoalVersion, ContextID: invocation.ContextID, ContextHash: invocation.ContextHash, Status: model.StatusRunning}},
		Approvals:       map[string]model.ApprovalRequest{},
	}
	return invocation, state
}

func approvalTraceRuntimeFixture(t *testing.T) (skillruntime.Invocation, model.ProjectState) {
	t.Helper()
	invocation, state := traceRuntimeFixture(t)
	input := json.RawMessage(`{"patch_sha256":"patch-hash"}`)
	digest := sha256.Sum256(input)
	invocation.ID, invocation.TraceID = "INV-APPROVAL-001", "TRC-APPROVAL-001"
	invocation.SkillName, invocation.SkillVersion, invocation.LogicalActorID = "patch", "1.0.0", "AGT-BUILD"
	invocation.RuntimePrincipalID, invocation.RuntimeBindingRevision = "runtime-build", 2
	invocation.Input, invocation.InputSHA256 = input, hex.EncodeToString(digest[:])
	state.Agents = map[string]model.LogicalAgent{invocation.LogicalActorID: {ID: invocation.LogicalActorID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active"}}
	state.RuntimeBindings = map[string][]model.RuntimeBinding{invocation.LogicalActorID: {{LogicalActorID: invocation.LogicalActorID, Revision: invocation.RuntimeBindingRevision, EnvironmentID: invocation.EnvironmentID, RuntimePrincipalID: invocation.RuntimePrincipalID, AgentTeamsInstanceID: invocation.AgentTeamsInstanceID, Status: "active"}}}
	mission := state.Missions[invocation.MissionID]
	mission.AllowedSkills, mission.RiskLevel = []model.MissionSkillGrant{{Name: invocation.SkillName, Version: invocation.SkillVersion}}, "L2"
	state.Missions[invocation.MissionID] = mission
	lease := state.Leases[invocation.LeaseID]
	lease.SubjectID, lease.AllowedSkills = invocation.LogicalActorID, []string{invocation.SkillName}
	state.Leases[invocation.LeaseID] = lease
	return invocation, state
}

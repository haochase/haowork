package skillruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestRuntimeInvokesAdapterOnceAndRecordsResultOnce(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	adapter := &countingAdapter{output: []byte(`{"ok":true}`), artifacts: []model.ArtifactRef{{Kind: "report", URI: "artifact://one", SHA256: "abc"}}}
	audit := &countingAudit{}
	runtime := runtimeFor(state, definition, adapter, audit)

	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if adapter.calls != 1 || audit.calls != 1 || result.Status != ResultSucceeded {
		t.Fatalf("calls/result = %d/%d/%#v, want 1/1/succeeded", adapter.calls, audit.calls, result)
	}
}

func TestRuntimeRejectsInputHashMismatchBeforeAdapter(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	invocation.InputSHA256 = "forged"
	adapter := &countingAdapter{}
	audit := &countingAudit{}
	runtime := runtimeFor(state, definition, adapter, audit)

	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil || result.ErrorCode != CodeInputHashMismatch || adapter.calls != 0 || audit.calls != 0 {
		t.Fatalf("mismatch result/calls = %#v, %v, %d/%d", result, err, adapter.calls, audit.calls)
	}
}

func TestRuntimeFailureNeverProducesSuccessResult(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	adapter := &countingAdapter{err: errors.New("adapter unavailable"), artifacts: []model.ArtifactRef{{Kind: "forbidden", URI: "artifact://no", SHA256: "no"}}}
	audit := &countingAudit{}
	runtime := runtimeFor(state, definition, adapter, audit)

	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if adapter.calls != 1 || audit.calls != 1 || result.Status == ResultSucceeded || len(result.Artifacts) != 0 {
		t.Fatalf("failed result/calls = %#v, %d/%d", result, adapter.calls, audit.calls)
	}
}

func TestRuntimeAuditFailureFailsClosedWithoutAdapterOutput(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	adapter := &countingAdapter{output: []byte(`{"ok":true}`), artifacts: []model.ArtifactRef{{Kind: "report", URI: "artifact://one", SHA256: "abc"}}}
	audit := &countingAudit{err: errors.New("ledger unavailable")}
	runtime := runtimeFor(state, definition, adapter, audit)

	result, err := runtime.Invoke(context.Background(), invocation)
	if err == nil || adapter.calls != 1 || audit.calls != 1 || result.Status != ResultFailed || len(result.Output) != 0 || result.OutputSHA256 != "" || len(result.Artifacts) != 0 {
		t.Fatalf("audit failure result/calls = %#v, %v, %d/%d", result, err, adapter.calls, audit.calls)
	}
}

func TestRuntimeRejectsUnknownSkillWithoutAdapter(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	invocation.SkillName = "unknown"
	adapter := &countingAdapter{}
	audit := &countingAudit{}
	runtime := runtimeFor(state, definition, adapter, audit)

	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil || result.Status != ResultRejected || result.ErrorCode != CodeDefinitionMismatch || adapter.calls != 0 || audit.calls != 0 {
		t.Fatalf("unknown skill result/calls = %#v, %v, %d/%d", result, err, adapter.calls, audit.calls)
	}
}

func TestRuntimeReevaluatesAuthorizationForEveryInvocation(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	adapter := &countingAdapter{output: []byte(`{"ok":true}`)}
	audit := &countingAudit{}
	runtime := runtimeFor(state, definition, adapter, audit)

	if _, err := runtime.Invoke(context.Background(), invocation); err != nil {
		t.Fatalf("first Invoke() error = %v", err)
	}
	invocation.SkillName = "unknown"
	result, err := runtime.Invoke(context.Background(), invocation)
	if err != nil || result.Status != ResultRejected || result.ErrorCode != CodeDefinitionMismatch || adapter.calls != 1 || audit.calls != 1 {
		t.Fatalf("reused authorization result/calls = %#v, %v, %d/%d", result, err, adapter.calls, audit.calls)
	}
}

func TestRuntimeFailsClosedWhenTraceLedgerIsNotConfigured(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	runtime := Runtime{Policy: Policy{Registry: registryForDefinition(definition), State: StaticState(state)}, Adapter: &countingAdapter{output: []byte(`{"ok":true}`)}, Audit: &countingAudit{}}
	result, err := runtime.Invoke(context.Background(), invocation)
	if err == nil || result.Status != ResultFailed || result.ErrorCode != CodeTraceFailed {
		t.Fatalf("result/error = %#v / %v, want trace failure", result, err)
	}
}

func TestRuntimeRecordsTerminalTraceWhenAllowedAdapterOrAuditIsMissing(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	tracer := &lifecycleTracer{}
	runtime := Runtime{Policy: Policy{Registry: registryForDefinition(definition), State: StaticState(state)}, Tracer: tracer}
	result, err := runtime.Invoke(context.Background(), invocation)
	if err == nil || result.Status != ResultFailed || result.ErrorCode != CodeAdapterFailed {
		t.Fatalf("result/error = %#v / %v, want adapter configuration failure", result, err)
	}
	if len(tracer.events) != 2 || tracer.events[0] != "policy" || tracer.events[1] != "adapter_finished:failed" {
		t.Fatalf("trace events = %#v, want policy plus terminal adapter failure", tracer.events)
	}
}

func runtimeFor(state model.ProjectState, definition Definition, adapter Adapter, audit AuditSink) Runtime {
	return Runtime{Policy: Policy{Registry: registryForDefinition(definition), State: StaticState(state)}, Adapter: adapter, Audit: audit, Tracer: acceptingTracer{}}
}

type acceptingTracer struct{}

func (acceptingTracer) PolicyDecision(context.Context, Invocation, Decision) error { return nil }
func (acceptingTracer) ApprovalWait(context.Context, Invocation, Decision) error   { return nil }
func (acceptingTracer) AdapterStarted(context.Context, Invocation, Decision) error { return nil }
func (acceptingTracer) AdapterFinished(context.Context, Invocation, Decision, Result) error {
	return nil
}
func (acceptingTracer) AuditResult(context.Context, Invocation, Decision, Result, error) error {
	return nil
}
func (acceptingTracer) Promote(context.Context, Invocation, Decision, Result) error { return nil }

type lifecycleTracer struct{ events []string }

func (tracer *lifecycleTracer) PolicyDecision(context.Context, Invocation, Decision) error {
	tracer.events = append(tracer.events, "policy")
	return nil
}
func (*lifecycleTracer) ApprovalWait(context.Context, Invocation, Decision) error   { return nil }
func (*lifecycleTracer) AdapterStarted(context.Context, Invocation, Decision) error { return nil }
func (tracer *lifecycleTracer) AdapterFinished(_ context.Context, _ Invocation, _ Decision, result Result) error {
	tracer.events = append(tracer.events, "adapter_finished:"+result.Status)
	return nil
}
func (*lifecycleTracer) AuditResult(context.Context, Invocation, Decision, Result, error) error {
	return nil
}
func (*lifecycleTracer) Promote(context.Context, Invocation, Decision, Result) error { return nil }

type countingAdapter struct {
	calls     int
	output    []byte
	artifacts []model.ArtifactRef
	err       error
}

func (adapter *countingAdapter) Invoke(context.Context, Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	adapter.calls++
	return adapter.output, adapter.artifacts, adapter.err
}

type countingAudit struct {
	calls int
	err   error
}

func (audit *countingAudit) RecordSkillCall(context.Context, Invocation, Result) error {
	audit.calls++
	return audit.err
}

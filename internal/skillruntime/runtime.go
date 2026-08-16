package skillruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

type Runtime struct {
	Policy  Policy
	Adapter Adapter
	Audit   AuditSink
	Tracer  ExecutionTracer
}

func (runtime Runtime) Invoke(ctx context.Context, invocation Invocation) (Result, error) {
	if runtime.Tracer == nil {
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, errors.New("skill runtime trace ledger is required")
	}
	decision, err := runtime.Policy.Evaluate(ctx, invocation)
	if err != nil {
		result := Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodePolicyFailed}
		if traceErr := runtime.recordPolicy(ctx, invocation, Decision{Status: DecisionDenied, Code: CodePolicyFailed}, result); traceErr != nil {
			return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, traceErr
		}
		return result, err
	}
	if err := runtime.Tracer.PolicyDecision(ctx, invocation, decision); err != nil {
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, err
	}
	if decision.Status != DecisionAllow {
		code := decision.Code
		if code == "" {
			code = CodePolicyDenied
		}
		result := Result{InvocationID: invocation.ID, Status: ResultRejected, ErrorCode: code}
		if decision.Status == DecisionApprovalRequired {
			if err := runtime.Tracer.ApprovalWait(ctx, invocation, decision); err != nil {
				return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, err
			}
		}
		return result, nil
	}
	if runtime.Adapter == nil || runtime.Audit == nil {
		result := Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeAdapterFailed}
		if traceErr := runtime.Tracer.AdapterFinished(ctx, invocation, decision, result); traceErr != nil {
			return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, traceErr
		}
		return result, errors.New("skill runtime adapter and audit sink are required")
	}
	if err := runtime.Tracer.AdapterStarted(ctx, invocation, decision); err != nil {
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, err
	}
	output, artifacts, err := runtime.Adapter.Invoke(ctx, invocation)
	result := Result{InvocationID: invocation.ID}
	if err != nil {
		result.Status = ResultFailed
		result.ErrorCode = CodeAdapterFailed
	} else {
		digest := sha256.Sum256(output)
		result.Status = ResultSucceeded
		result.Output = append([]byte(nil), output...)
		result.OutputSHA256 = hex.EncodeToString(digest[:])
		result.Artifacts = append(result.Artifacts, artifacts...)
	}
	if traceErr := runtime.Tracer.AdapterFinished(ctx, invocation, decision, result); traceErr != nil {
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, traceErr
	}
	if auditErr := runtime.Audit.RecordSkillCall(ctx, invocation, result); auditErr != nil {
		if traceErr := runtime.Tracer.AuditResult(ctx, invocation, decision, result, auditErr); traceErr != nil {
			return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, traceErr
		}
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeAuditFailed}, auditErr
	}
	if err := runtime.Tracer.AuditResult(ctx, invocation, decision, result, nil); err != nil {
		return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, err
	}
	if result.Status == ResultSucceeded {
		if err := runtime.Tracer.Promote(ctx, invocation, decision, result); err != nil {
			return Result{InvocationID: invocation.ID, Status: ResultFailed, ErrorCode: CodeTraceFailed}, err
		}
	}
	return result, nil
}

func (runtime Runtime) recordPolicy(ctx context.Context, invocation Invocation, decision Decision, _ Result) error {
	return runtime.Tracer.PolicyDecision(ctx, invocation, decision)
}

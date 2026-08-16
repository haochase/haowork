package skillruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestPolicyRejectsSkillOutsideMissionLeaseScopeOrEnvironment(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}

	invocation.Scope = []string{"internal/other"}
	if decision, err := policy.Evaluate(context.Background(), invocation); err != nil || decision.Status != DecisionDenied || decision.Code != CodeScopeDenied {
		t.Fatalf("scope decision = %#v, %v; want scope denial", decision, err)
	}

	invocation = policyInvocation(t)
	invocation.EnvironmentID = "internal"
	mission := state.Missions[invocation.MissionID]
	mission.EnvironmentID = invocation.EnvironmentID
	state.Missions[invocation.MissionID] = mission
	lease := state.Leases[invocation.LeaseID]
	lease.EnvironmentID = invocation.EnvironmentID
	state.Leases[invocation.LeaseID] = lease
	bindings := state.RuntimeBindings[invocation.LogicalActorID]
	bindings[1].EnvironmentID = invocation.EnvironmentID
	state.RuntimeBindings[invocation.LogicalActorID] = bindings
	if decision, err := policy.Evaluate(context.Background(), invocation); err != nil || decision.Status != DecisionDenied || decision.Code != CodeEnvironmentDenied {
		t.Fatalf("environment decision = %#v, %v; want environment denial", decision, err)
	}
}

func TestPolicyRequiresHashBoundHumanApprovalForL2AndL3(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL2)
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}
	decision, err := policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionApprovalRequired || decision.ApprovalID != invocation.InputSHA256 {
		t.Fatalf("unapproved L2 decision = %#v, %v; want hash-bound approval", decision, err)
	}

	state.Approvals["APR-001"] = model.ApprovalRequest{ID: "APR-001", SubjectType: "skill", SubjectID: invocation.SkillName, PayloadSHA256: invocation.InputSHA256, RiskLevel: "L2", RequesterID: invocation.LogicalActorID, DeciderID: "USR-LEAD", Status: "approved"}
	decision, err = policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionAllow {
		t.Fatalf("approved L2 decision = %#v, %v; want Allow", decision, err)
	}

	state, definition, invocation = policyFixture(t, RiskL3)
	state.Agents["AGT-VERIFY"] = model.LogicalAgent{ID: "AGT-VERIFY", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionVerify, Status: "active"}
	state.Approvals["APR-AGENT"] = model.ApprovalRequest{ID: "APR-AGENT", SubjectType: "skill", SubjectID: invocation.SkillName, PayloadSHA256: invocation.InputSHA256, RiskLevel: "L3", RequesterID: invocation.LogicalActorID, DeciderID: "AGT-VERIFY", Status: "approved"}
	policy = Policy{Registry: registryForDefinition(definition), State: StaticState(state)}
	decision, err = policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionApprovalRequired {
		t.Fatalf("agent approval decision = %#v, %v; want ApprovalRequired", decision, err)
	}
}

func TestPolicyNeverTreatsAgentFunctionAsGovernanceRole(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	state.Agents[invocation.LogicalActorID] = model.LogicalAgent{ID: invocation.LogicalActorID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active"}
	definition.AllowedFunctions = []model.AgentFunction{model.FunctionVerify}
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}

	decision, err := policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionDenied || decision.Code != CodeFunctionDenied {
		t.Fatalf("function decision = %#v, %v; want function denial", decision, err)
	}
}

func TestPolicyRequiresRegistryDefinitionMatchingInvocation(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	definition.Name = "record"
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}

	decision, err := policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionDenied || decision.Code != CodeDefinitionMismatch {
		t.Fatalf("definition decision = %#v, %v; want definition mismatch", decision, err)
	}
}

func TestPolicyRejectsStaleBindingBeforeUnknownSkill(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	invocation.SkillName = "unknown"
	invocation.RuntimeBindingRevision++
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}

	decision, err := policy.Evaluate(context.Background(), invocation)
	if err != nil || decision.Status != DecisionDenied || decision.Code != "runtime_binding_mismatch" {
		t.Fatalf("decision = %#v, %v; want runtime binding mismatch before definition lookup", decision, err)
	}
}

func TestPolicyRejectsLeaseOutsideInjectedClockWindow(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state), Clock: ClockFunc(func() time.Time { return now })}

	for name, mutate := range map[string]func(*model.Lease){
		"zero window":  func(lease *model.Lease) { lease.StartsAt, lease.ExpiresAt = time.Time{}, time.Time{} },
		"future start": func(lease *model.Lease) { lease.StartsAt, lease.ExpiresAt = now.Add(time.Minute), now.Add(time.Hour) },
		"expired":      func(lease *model.Lease) { lease.StartsAt, lease.ExpiresAt = now.Add(-time.Hour), now.Add(-time.Minute) },
		"reversed":     func(lease *model.Lease) { lease.StartsAt, lease.ExpiresAt = now.Add(time.Hour), now.Add(time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := state
			candidate.Leases = map[string]model.Lease{}
			lease := state.Leases[invocation.LeaseID]
			mutate(&lease)
			candidate.Leases[lease.ID] = lease
			policy.State = StaticState(candidate)

			decision, err := policy.Evaluate(context.Background(), invocation)
			if err != nil || decision.Status != DecisionDenied || decision.Code != CodeLeaseMismatch {
				t.Fatalf("lease decision = %#v, %v; want lease mismatch", decision, err)
			}
		})
	}
}

func TestPolicyRequiresFullRuntimeBindingAndCurrentRun(t *testing.T) {
	state, definition, invocation := policyFixture(t, RiskL1)
	policy := Policy{Registry: registryForDefinition(definition), State: StaticState(state)}
	for name, mutate := range map[string]func(*Invocation, *model.ProjectState){
		"runtime principal": func(invocation *Invocation, _ *model.ProjectState) {
			invocation.RuntimePrincipalID = "forged-principal"
		},
		"agentteams instance": func(invocation *Invocation, _ *model.ProjectState) {
			invocation.AgentTeamsInstanceID = "forged-instance"
		},
		"missing run": func(invocation *Invocation, state *model.ProjectState) { delete(state.Runs, invocation.RunID) },
		"run task mismatch": func(invocation *Invocation, state *model.ProjectState) {
			run := state.Runs[invocation.RunID]
			run.TaskID = "TSK-OTHER"
			state.Runs[invocation.RunID] = run
		},
		"run context mismatch": func(invocation *Invocation, state *model.ProjectState) {
			run := state.Runs[invocation.RunID]
			run.ContextHash = "other"
			state.Runs[invocation.RunID] = run
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := clonePolicyState(state)
			candidateInvocation := invocation
			mutate(&candidateInvocation, &candidate)
			policy.State = StaticState(candidate)
			decision, err := policy.Evaluate(context.Background(), candidateInvocation)
			if err != nil || decision.Status != DecisionDenied {
				t.Fatalf("decision = %#v, %v; want full binding denial", decision, err)
			}
		})
	}
}

func policyFixture(t *testing.T, risk RiskLevel) (model.ProjectState, Definition, Invocation) {
	t.Helper()
	input := json.RawMessage(`{"operation":"write"}`)
	digest := sha256.Sum256(input)
	invocation := Invocation{ID: "INV-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001", RunID: "RUN-001", LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", AgentTeamsInstanceID: "AT-001", RuntimeBindingRevision: 2, SkillName: "patch", SkillVersion: "1.0.0", EnvironmentID: "public", TraceID: "TRC-001", GoalVersion: 7, ContextID: "CTX-001", ContextHash: "context-hash", LeaseID: "LSE-001", Scope: []string{"internal/skillruntime"}, Input: input, InputSHA256: hex.EncodeToString(digest[:])}
	mission := model.MissionEnvelope{ID: invocation.MissionID, GoalVersion: invocation.GoalVersion, ContextID: invocation.ContextID, ContextHash: invocation.ContextHash, LeaseID: invocation.LeaseID, AllowedScopes: []string{"internal/skillruntime"}, AllowedSkills: []model.MissionSkillGrant{{Name: invocation.SkillName, Version: invocation.SkillVersion}}, EnvironmentID: invocation.EnvironmentID, RiskLevel: string(risk)}
	state := model.ProjectState{
		Goal:            model.GoalVersion{Version: invocation.GoalVersion},
		Agents:          map[string]model.LogicalAgent{invocation.LogicalActorID: {ID: invocation.LogicalActorID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active"}},
		RuntimeBindings: map[string][]model.RuntimeBinding{invocation.LogicalActorID: {{LogicalActorID: invocation.LogicalActorID, Revision: 1, EnvironmentID: "old", Status: "inactive"}, {LogicalActorID: invocation.LogicalActorID, Revision: invocation.RuntimeBindingRevision, EnvironmentID: invocation.EnvironmentID, RuntimePrincipalID: invocation.RuntimePrincipalID, AgentTeamsInstanceID: invocation.AgentTeamsInstanceID, Status: "active"}}},
		Missions:        map[string]model.MissionEnvelope{invocation.MissionID: mission},
		Contexts:        map[string]model.ContextSlice{invocation.ContextID: {ID: invocation.ContextID, TaskID: invocation.TaskID, GoalVersion: invocation.GoalVersion, SliceHash: invocation.ContextHash}},
		Leases:          map[string]model.Lease{invocation.LeaseID: {ID: invocation.LeaseID, TaskID: invocation.TaskID, SubjectKind: "logical_agent", SubjectID: invocation.LogicalActorID, EnvironmentID: invocation.EnvironmentID, ContextID: invocation.ContextID, GoalVersion: invocation.GoalVersion, AllowedScopes: []string{"internal/skillruntime"}, AllowedSkills: []string{invocation.SkillName}, Status: "active", StartsAt: time.Now().UTC().Add(-time.Hour), ExpiresAt: time.Now().UTC().Add(time.Hour)}},
		Runs:            map[string]model.Run{invocation.RunID: {ID: invocation.RunID, TaskID: invocation.TaskID, GoalVersion: invocation.GoalVersion, ContextID: invocation.ContextID, ContextHash: invocation.ContextHash, Status: model.StatusRunning}},
		Approvals:       map[string]model.ApprovalRequest{},
	}
	return state, Definition{Name: invocation.SkillName, Version: invocation.SkillVersion, Risk: risk, AllowedFunctions: []model.AgentFunction{model.FunctionBuild}, RequiredContext: true, RequiredLease: true, SupportedEnvironments: []string{invocation.EnvironmentID}}, invocation
}

func clonePolicyState(state model.ProjectState) model.ProjectState {
	encoded, _ := json.Marshal(state)
	var clone model.ProjectState
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

func policyInvocation(t *testing.T) Invocation {
	t.Helper()
	_, _, invocation := policyFixture(t, RiskL1)
	return invocation
}

func registryForDefinition(definition Definition) *Registry {
	return &Registry{definitions: map[string]map[string]Definition{
		definition.Name: {definition.Version: definition},
	}}
}

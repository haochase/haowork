package skillruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

type Policy struct {
	Registry *Registry
	State    StateReader
	Clock    Clock
}

func (policy Policy) Evaluate(ctx context.Context, invocation Invocation) (Decision, error) {
	if !matchesInputHash(invocation) {
		return denied(CodeInputHashMismatch), nil
	}
	if policy.State == nil {
		return Decision{}, errors.New("skill policy state reader is required")
	}
	state, err := policy.State.State(ctx)
	if err != nil {
		return Decision{}, err
	}
	mission, exists := state.Missions[invocation.MissionID]
	if !exists {
		return denied("mission_not_active"), nil
	}
	if state.Goal.Version != invocation.GoalVersion || mission.GoalVersion != invocation.GoalVersion {
		return denied("goal_version_mismatch"), nil
	}
	agent, binding, ok := activeBinding(state, invocation.LogicalActorID)
	if !ok || binding.Revision != invocation.RuntimeBindingRevision || binding.RuntimePrincipalID != invocation.RuntimePrincipalID || binding.AgentTeamsInstanceID != invocation.AgentTeamsInstanceID {
		return denied("runtime_binding_mismatch"), nil
	}
	if agent.SubjectKind != model.ActorAgent || agent.GovernanceRole != model.RoleAgent || agent.Status != "active" {
		return denied("logical_agent_invalid"), nil
	}
	if !matchesCurrentRun(state, invocation) {
		return denied("run_mismatch"), nil
	}
	if policy.Registry == nil {
		return denied(CodeDefinitionMismatch), nil
	}
	definition, err := policy.Registry.Resolve(invocation.SkillName, invocation.SkillVersion)
	if err != nil || definition.Name != invocation.SkillName || definition.Version != invocation.SkillVersion || mission.RiskLevel != string(definition.Risk) {
		return denied(CodeDefinitionMismatch), nil
	}
	if !containsFunction(definition.AllowedFunctions, agent.Function) {
		return denied(CodeFunctionDenied), nil
	}
	if definition.RequiredContext && !matchesCurrentContext(state, mission, invocation) {
		return denied("context_mismatch"), nil
	}
	if definition.RequiredLease && !matchesActiveLease(state, mission, invocation, policy.now()) {
		return denied(CodeLeaseMismatch), nil
	}
	scope, ok := normalizeScope(invocation.Scope)
	if !ok || !containedBy(scope, mission.AllowedScopes) || (definition.RequiredLease && !containedBy(scope, state.Leases[invocation.LeaseID].AllowedScopes)) {
		return denied(CodeScopeDenied), nil
	}
	if invocation.EnvironmentID != mission.EnvironmentID || binding.EnvironmentID != invocation.EnvironmentID || !containsString(definition.SupportedEnvironments, invocation.EnvironmentID) {
		return denied(CodeEnvironmentDenied), nil
	}
	if !missionAllowsSkill(mission, invocation) || (definition.RequiredLease && !leaseAllowsSkill(state.Leases[invocation.LeaseID], invocation.SkillName)) {
		return denied("skill_not_granted"), nil
	}
	if definition.Risk != RiskL0 && definition.Risk != RiskL1 && definition.Risk != RiskL2 && definition.Risk != RiskL3 {
		return denied("risk_invalid"), nil
	}
	if definition.Risk == RiskL0 || definition.Risk == RiskL1 {
		return allowed(agent.Function), nil
	}
	if hasHumanApproval(state, invocation, definition.Risk) {
		return allowed(agent.Function), nil
	}
	return Decision{Status: DecisionApprovalRequired, Code: CodeApprovalRequired, ApprovalID: invocation.InputSHA256, AgentFunction: agent.Function}, nil
}

func denied(code string) Decision {
	return Decision{Status: DecisionDenied, Code: code}
}

func allowed(function model.AgentFunction) Decision {
	return Decision{Status: DecisionAllow, AgentFunction: function}
}

func matchesInputHash(invocation Invocation) bool {
	digest := sha256.Sum256(invocation.Input)
	return strings.TrimSpace(invocation.InputSHA256) != "" && invocation.InputSHA256 == hex.EncodeToString(digest[:])
}

func activeBinding(state model.ProjectState, agentID string) (model.LogicalAgent, model.RuntimeBinding, bool) {
	agent, exists := state.Agents[agentID]
	if !exists {
		return model.LogicalAgent{}, model.RuntimeBinding{}, false
	}
	bindings := state.RuntimeBindings[agentID]
	active := make([]model.RuntimeBinding, 0, 1)
	for _, binding := range bindings {
		if binding.LogicalActorID == agentID && binding.Status == "active" {
			active = append(active, binding)
		}
	}
	if len(active) != 1 {
		return model.LogicalAgent{}, model.RuntimeBinding{}, false
	}
	return agent, active[0], true
}

func matchesCurrentContext(state model.ProjectState, mission model.MissionEnvelope, invocation Invocation) bool {
	contextSlice, exists := state.Contexts[invocation.ContextID]
	return exists && !contextSlice.Superseded && contextSlice.ID == mission.ContextID && contextSlice.TaskID == invocation.TaskID && contextSlice.GoalVersion == invocation.GoalVersion && contextSlice.SliceHash == invocation.ContextHash && mission.ContextHash == invocation.ContextHash
}

func matchesCurrentRun(state model.ProjectState, invocation Invocation) bool {
	run, exists := state.Runs[invocation.RunID]
	return exists && run.ID == invocation.RunID && run.TaskID == invocation.TaskID && run.GoalVersion == invocation.GoalVersion && run.ContextID == invocation.ContextID && run.ContextHash == invocation.ContextHash && run.Status == model.StatusRunning
}

func matchesActiveLease(state model.ProjectState, mission model.MissionEnvelope, invocation Invocation, now time.Time) bool {
	lease, exists := state.Leases[invocation.LeaseID]
	return exists && lease.Status == "active" && lease.ID == mission.LeaseID && lease.TaskID == invocation.TaskID && lease.SubjectID == invocation.LogicalActorID && lease.ContextID == invocation.ContextID && lease.GoalVersion == invocation.GoalVersion && lease.EnvironmentID == invocation.EnvironmentID && activeLeaseWindow(lease, now)
}

func activeLeaseWindow(lease model.Lease, now time.Time) bool {
	return !now.IsZero() && !lease.StartsAt.IsZero() && !lease.ExpiresAt.IsZero() && lease.ExpiresAt.After(lease.StartsAt) && !now.Before(lease.StartsAt) && now.Before(lease.ExpiresAt)
}

func (policy Policy) now() time.Time {
	if policy.Clock != nil {
		return policy.Clock.Now().UTC()
	}
	return time.Now().UTC()
}

func normalizeScope(scope []string) ([]string, bool) {
	if len(scope) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(scope))
	normalized := make([]string, 0, len(scope))
	for _, value := range scope {
		value = strings.TrimSpace(value)
		if value == "" || filepath.IsAbs(value) {
			return nil, false
		}
		clean := filepath.ToSlash(filepath.Clean(value))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, false
		}
		if _, exists := seen[clean]; !exists {
			seen[clean] = struct{}{}
			normalized = append(normalized, clean)
		}
	}
	sort.Strings(normalized)
	return normalized, true
}

func containedBy(scope, allowed []string) bool {
	normalizedAllowed, ok := normalizeScope(allowed)
	if !ok {
		return false
	}
	for _, requested := range scope {
		allowedHere := false
		for _, candidate := range normalizedAllowed {
			if requested == candidate || strings.HasPrefix(requested, candidate+"/") {
				allowedHere = true
				break
			}
		}
		if !allowedHere {
			return false
		}
	}
	return true
}

func containsFunction(values []model.AgentFunction, expected model.AgentFunction) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}

func missionAllowsSkill(mission model.MissionEnvelope, invocation Invocation) bool {
	for _, grant := range mission.AllowedSkills {
		if grant.Name == invocation.SkillName && grant.Version == invocation.SkillVersion {
			return true
		}
	}
	return false
}

func leaseAllowsSkill(lease model.Lease, name string) bool {
	for _, allowed := range lease.AllowedSkills {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

func hasHumanApproval(state model.ProjectState, invocation Invocation, risk RiskLevel) bool {
	for _, approval := range state.Approvals {
		if approval.Status != "approved" || approval.SubjectType != "skill" || approval.SubjectID != invocation.SkillName || approval.PayloadSHA256 != invocation.InputSHA256 || approval.RiskLevel != string(risk) || approval.RequesterID == approval.DeciderID {
			continue
		}
		if _, isAgent := state.Agents[approval.DeciderID]; !isAgent {
			return true
		}
	}
	return false
}

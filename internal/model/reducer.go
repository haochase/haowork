package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func Reduce(events []Event) (ProjectState, error) {
	var state ProjectState
	seenEventIDs := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, exists := seenEventIDs[event.ID]; exists {
			return ProjectState{}, fmt.Errorf("event %s sequence %d: duplicate event id %q", event.ID, event.Sequence, event.ID)
		}
		seenEventIDs[event.ID] = struct{}{}

		if state.ProjectID != "" {
			if event.ProjectID != state.ProjectID {
				return ProjectState{}, fmt.Errorf("event %s sequence %d: project id %q does not match %q", event.ID, event.Sequence, event.ProjectID, state.ProjectID)
			}
			if event.GoalVersion != state.Goal.Version {
				return ProjectState{}, fmt.Errorf("event %s sequence %d: goal version %d does not match %d", event.ID, event.Sequence, event.GoalVersion, state.Goal.Version)
			}
		}

		var err error
		switch event.Type {
		case "project.initialized":
			err = applyProjectInitialized(&state, event)
		case "goal.change.proposed":
			err = applyGoalChangeProposed(&state, event)
		case "goal.change.approved":
			err = applyGoalChangeApproved(&state, event)
		case "goal.change.rejected":
			err = applyGoalChangeRejected(&state, event)
		case "lease.issued":
			err = applyLeaseIssued(&state, event)
		case "lease.renewed":
			err = applyLeaseRenewed(&state, event)
		case "lease.released":
			err = applyLeaseReleased(&state, event)
		case "lease.revoked":
			err = applyLeaseRevoked(&state, event)
		case "agent.identity.registered":
			err = applyAgentIdentityRegistered(&state, event)
		case "agent.runtime.bound":
			err = applyRuntimeBound(&state, event)
		case "agent.runtime.unbound":
			err = applyRuntimeUnbound(&state, event)
		case "capsule.imported":
			err = applyCapsuleImported(&state, event)
		case "mission.issued":
			err = applyMissionIssued(&state, event)
		case "mission.invalidated":
			err = applyMissionInvalidated(&state, event)
		case "approval.requested":
			err = applyApprovalRequested(&state, event)
		case "approval.decided":
			err = applyApprovalDecided(&state, event)
		case "approval.invalidated":
			err = applyApprovalInvalidated(&state, event)
		case "conflict.opened":
			err = applyConflictOpened(&state, event)
		case "conflict.resolved":
			err = applyConflictResolved(&state, event)
		case "requirement.planned":
			err = applyRequirementPlanned(&state, event)
		case "requirement.approved":
			err = applyRequirementApproved(&state, event)
		case "context.issued":
			err = applyContextIssued(&state, event)
		case "context.superseded":
			err = applyContextSuperseded(&state, event)
		case "run.started":
			err = applyRunStarted(&state, event)
		case "run.finished":
			err = applyRunFinished(&state, event)
		case "run.resumed":
			err = applyRunResumed(&state, event)
		case "run.step.started":
			err = applyRunStepStarted(&state, event)
		case "run.step.finished":
			err = applyRunStepFinished(&state, event)
		case "checkpoint.created":
			err = applyCheckpointCreated(&state, event)
		case "executor.event.received":
			err = applyExecutorEventReceived(&state, event)
		case "evidence.recorded":
			err = applyEvidenceRecorded(&state, event)
		case "evidence.candidate.recorded":
			err = applyEvidenceCandidateRecorded(&state, event)
		case "evidence.verified":
			err = applyEvidenceVerified(&state, event)
		case "evidence.invalidated":
			err = applyEvidenceInvalidated(&state, event)
		case "task.completed":
			err = applyTaskCompleted(&state, event)
		case "changes.scanned":
			err = applyChangesScanned(&state, event)
		case "change.attributed":
			err = applyChangeAttributed(&state, event)
		default:
			err = fmt.Errorf("unknown event type %q", event.Type)
		}
		if err != nil {
			return ProjectState{}, fmt.Errorf("event %s sequence %d: %w", event.ID, event.Sequence, err)
		}
	}
	return state, nil
}

func applyProjectInitialized(state *ProjectState, event Event) error {
	if state.ProjectID != "" {
		return errors.New("project is already initialized")
	}
	var payload ProjectInitialized
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.ProjectID == "" {
		return errors.New("project id is required")
	}
	if strings.TrimSpace(payload.Goal.Statement) == "" {
		return errors.New("goal statement is required")
	}
	if !hasNonEmpty(payload.Goal.CompletionCriteria) {
		return errors.New("at least one completion criterion is required")
	}
	if payload.Goal.Version != 1 {
		return errors.New("goal version must be 1")
	}
	if event.GoalVersion != payload.Goal.Version {
		return fmt.Errorf("event goal version %d does not match initialized goal version %d", event.GoalVersion, payload.Goal.Version)
	}

	state.ProjectID = event.ProjectID
	state.Goal = payload.Goal
	state.GoalChanges = make(map[string]GoalChange)
	state.Leases = make(map[string]Lease)
	state.Conflicts = make(map[string]Conflict)
	state.Requirements = make(map[string]Requirement)
	state.Tasks = make(map[string]Task)
	state.Runs = make(map[string]Run)
	state.Evidence = make(map[string][]Evidence)
	state.Contexts = make(map[string]ContextSlice)
	state.Steps = make(map[string]Step)
	state.Checkpoints = make(map[string]Checkpoint)
	state.ExecutorEvents = make(map[string]ExecutorEvent)
	state.Changes = make(map[string]FileChange)
	state.Attributions = make(map[string]ChangeAttribution)
	state.Agents = make(map[string]LogicalAgent)
	state.RuntimeBindings = make(map[string][]RuntimeBinding)
	state.Missions = make(map[string]MissionEnvelope)
	state.Approvals = make(map[string]ApprovalRequest)
	return nil
}

func applyGoalChangeProposed(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload GoalChangeProposed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	change := payload.GoalChange
	if strings.TrimSpace(change.ID) == "" {
		return errors.New("goal change id is required")
	}
	if _, exists := state.GoalChanges[change.ID]; exists {
		return fmt.Errorf("goal change %q already exists", change.ID)
	}
	if change.BaseVersion != state.Goal.Version {
		return fmt.Errorf("goal change %q base version %d does not match %d", change.ID, change.BaseVersion, state.Goal.Version)
	}
	if change.Proposed.Version != state.Goal.Version+1 {
		return fmt.Errorf("goal change %q proposed version %d does not match next version %d", change.ID, change.Proposed.Version, state.Goal.Version+1)
	}
	if strings.TrimSpace(change.Proposed.Statement) == "" {
		return errors.New("proposed goal statement is required")
	}
	if !hasNonEmpty(change.Proposed.CompletionCriteria) {
		return errors.New("proposed goal requires at least one completion criterion")
	}
	change.Status = "proposed"
	change.DeciderID = ""
	change.DecidedAt = time.Time{}
	state.GoalChanges[change.ID] = change
	return nil
}

func applyGoalChangeApproved(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload GoalChangeApproved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	change, exists := state.GoalChanges[payload.GoalChangeID]
	if !exists {
		return fmt.Errorf("goal change %q not found", payload.GoalChangeID)
	}
	if change.Status != "proposed" {
		return fmt.Errorf("goal change %q status %q requires proposed", change.ID, change.Status)
	}
	if change.BaseVersion != state.Goal.Version || change.Proposed.Version != state.Goal.Version+1 {
		return fmt.Errorf("goal change %q no longer targets current goal version", change.ID)
	}
	change.Status = "approved"
	change.DeciderID = payload.DeciderID
	change.DecidedAt = payload.DecidedAt
	state.GoalChanges[change.ID] = change
	state.Goal = change.Proposed
	markOlderBindingsStale(state)
	return nil
}

func applyGoalChangeRejected(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload GoalChangeRejected
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	change, exists := state.GoalChanges[payload.GoalChangeID]
	if !exists {
		return fmt.Errorf("goal change %q not found", payload.GoalChangeID)
	}
	if change.Status != "proposed" {
		return fmt.Errorf("goal change %q status %q requires proposed", change.ID, change.Status)
	}
	change.Status = "rejected"
	change.DeciderID = payload.DeciderID
	change.DecidedAt = payload.DecidedAt
	state.GoalChanges[change.ID] = change
	return nil
}

func markOlderBindingsStale(state *ProjectState) {
	for id, context := range state.Contexts {
		if context.GoalVersion < state.Goal.Version {
			context.Superseded = true
			state.Contexts[id] = context
		}
	}
	for taskID, records := range state.Evidence {
		for index := range records {
			if records[index].GoalVersion < state.Goal.Version {
				records[index].Status = "stale"
			}
		}
		state.Evidence[taskID] = records
	}
	for id, lease := range state.Leases {
		if lease.GoalVersion < state.Goal.Version && !isTerminalLease(lease.Status) {
			lease.Status = "stale"
			state.Leases[id] = lease
		}
	}
}

func applyLeaseIssued(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload LeaseIssued
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	lease := payload.Lease
	if strings.TrimSpace(lease.ID) == "" {
		return errors.New("lease id is required")
	}
	if _, exists := state.Leases[lease.ID]; exists {
		return fmt.Errorf("lease %q already exists", lease.ID)
	}
	if lease.GoalVersion != state.Goal.Version {
		return fmt.Errorf("lease %q goal version %d does not match %d", lease.ID, lease.GoalVersion, state.Goal.Version)
	}
	if lease.Revision != 1 {
		return fmt.Errorf("lease %q revision %d must be 1 when issued", lease.ID, lease.Revision)
	}
	lease.Status = "active"
	state.Leases[lease.ID] = lease
	return nil
}

func applyLeaseRenewed(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload LeaseRenewed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	lease, err := activeLease(state, payload.LeaseID)
	if err != nil {
		return err
	}
	lease.Revision++
	lease.ExpiresAt = payload.ExpiresAt
	state.Leases[lease.ID] = lease
	return nil
}

func applyLeaseReleased(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload LeaseReleased
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	lease, err := activeLease(state, payload.LeaseID)
	if err != nil {
		return err
	}
	lease.Revision++
	lease.Status = "released"
	state.Leases[lease.ID] = lease
	return nil
}

func applyLeaseRevoked(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload LeaseRevoked
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	lease, err := activeLease(state, payload.LeaseID)
	if err != nil {
		return err
	}
	lease.Revision++
	lease.Status = "revoked"
	state.Leases[lease.ID] = lease
	return nil
}

func activeLease(state *ProjectState, leaseID string) (Lease, error) {
	lease, exists := state.Leases[leaseID]
	if !exists {
		return Lease{}, fmt.Errorf("lease %q not found", leaseID)
	}
	if isTerminalLease(lease.Status) {
		return Lease{}, fmt.Errorf("lease %q is terminal", leaseID)
	}
	return lease, nil
}

func isTerminalLease(status string) bool {
	switch status {
	case "released", "revoked", "stale":
		return true
	default:
		return false
	}
}

func applyAgentIdentityRegistered(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload AgentIdentityRegistered
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	agent := payload.Agent
	if strings.TrimSpace(agent.ID) == "" {
		return errors.New("logical agent id is required")
	}
	if event.AggregateType != "agent" || event.AggregateID != agent.ID {
		return errors.New("logical agent aggregate does not match payload")
	}
	if agent.SubjectKind != ActorAgent || agent.GovernanceRole != RoleAgent {
		return errors.New("logical agent must have agent subject kind and governance role")
	}
	if !validAgentFunction(agent.Function) {
		return fmt.Errorf("invalid agent function %q", agent.Function)
	}
	if _, exists := state.Agents[agent.ID]; exists {
		return fmt.Errorf("logical agent %q already exists", agent.ID)
	}
	agent.Status = "active"
	state.Agents[agent.ID] = agent
	return nil
}

func applyRuntimeBound(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	if event.Actor.Kind != ActorHuman || event.Actor.Role != RoleOwner {
		return errors.New("runtime binding requires a human owner")
	}
	var payload RuntimeBound
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	binding := payload.Binding
	if event.AggregateType != "agent" || event.AggregateID != binding.LogicalActorID {
		return errors.New("runtime binding aggregate does not match payload")
	}
	if _, exists := state.Agents[binding.LogicalActorID]; !exists {
		return fmt.Errorf("logical agent %q not found", binding.LogicalActorID)
	}
	if strings.TrimSpace(binding.EnvironmentID) == "" || strings.TrimSpace(binding.AgentTeamsInstanceID) == "" || strings.TrimSpace(binding.RuntimePrincipalID) == "" {
		return errors.New("runtime binding environment, instance, and principal are required")
	}
	history := state.RuntimeBindings[binding.LogicalActorID]
	if binding.Revision != len(history)+1 {
		return fmt.Errorf("runtime binding revision %d must be %d", binding.Revision, len(history)+1)
	}
	if len(history) > 0 {
		history[len(history)-1].Status = "inactive"
	}
	binding.Status = "active"
	state.RuntimeBindings[binding.LogicalActorID] = append(history, binding)
	return nil
}

func applyCapsuleImported(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	if event.Actor.Kind != ActorHuman || event.Actor.Role != RoleOwner {
		return errors.New("capsule import requires a human owner")
	}
	var payload CapsuleImported
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "capsule" || event.AggregateID != payload.PreviewHash || strings.TrimSpace(payload.PreviewHash) == "" || strings.TrimSpace(payload.Binding.LogicalActorID) == "" {
		return errors.New("capsule import aggregate or binding is invalid")
	}
	return nil
}

func applyRuntimeUnbound(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RuntimeUnbound
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "agent" || event.AggregateID != payload.LogicalActorID {
		return errors.New("runtime unbinding aggregate does not match payload")
	}
	history := state.RuntimeBindings[payload.LogicalActorID]
	if len(history) == 0 {
		return fmt.Errorf("logical agent %q has no runtime binding", payload.LogicalActorID)
	}
	current := &history[len(history)-1]
	if current.Status != "active" || current.Revision != payload.Revision {
		return errors.New("runtime binding is not active at requested revision")
	}
	current.Status = "inactive"
	state.RuntimeBindings[payload.LogicalActorID] = history
	return nil
}

func validAgentFunction(function AgentFunction) bool {
	switch function {
	case FunctionManager, FunctionDeliveryLeader, FunctionResearch, FunctionBuild, FunctionVerify:
		return true
	default:
		return false
	}
}

func applyMissionIssued(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload MissionIssued
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	mission := payload.Envelope
	if strings.TrimSpace(mission.ID) == "" {
		return errors.New("mission id is required")
	}
	if mission.ProjectID != state.ProjectID || mission.GoalVersion != state.Goal.Version {
		return errors.New("mission does not target the current project goal")
	}
	if event.AggregateType != "mission" || event.AggregateID != mission.ID {
		return errors.New("mission aggregate does not match envelope")
	}
	if mission.RiskLevel == "L3" && (event.Actor.Kind != ActorHuman || event.Actor.Role != RoleOwner) {
		return errors.New("L3 mission issuance requires a human owner")
	}
	if missionApprovalRequired(mission.RiskLevel) && !hasApprovedMissionApproval(*state, mission) {
		return errors.New("mission requires a matching approved approval request")
	}
	contextSlice, exists := state.Contexts[mission.ContextID]
	if !exists || contextSlice.Superseded || contextSlice.GoalVersion != state.Goal.Version || contextSlice.SliceHash != mission.ContextHash {
		return errors.New("mission context binding is not current")
	}
	lease, exists := state.Leases[mission.LeaseID]
	if !exists || lease.Status != "active" || lease.GoalVersion != state.Goal.Version || lease.ContextID != mission.ContextID || lease.EnvironmentID != mission.EnvironmentID {
		return errors.New("mission lease binding is not active")
	}
	if err := VerifyMissionEnvelope(mission, lease); err != nil {
		return err
	}
	if len(mission.GovernanceTaskIDs) != 1 {
		return errors.New("mission requires exactly one task binding")
	}
	taskID := mission.GovernanceTaskIDs[0]
	task, exists := state.Tasks[taskID]
	if !exists || task.Status != StatusApproved || task.GoalVersion != state.Goal.Version {
		return fmt.Errorf("mission task binding %q is not approved", taskID)
	}
	if contextSlice.TaskID != taskID || lease.TaskID != taskID {
		return errors.New("mission context and lease task bindings must match")
	}
	if !IsMissionLeaseBoundToBuildAssignment(lease, mission.RoleAssignments[FunctionBuild]) {
		return errors.New("mission lease subject is not bound to the build assignment")
	}
	matchingLeases := 0
	for _, candidate := range state.Leases {
		if IsActiveMissionLeaseForBuildAssignment(candidate, state.Goal.Version, mission.ContextID, mission.EnvironmentID, taskID, mission.RoleAssignments[FunctionBuild]) {
			matchingLeases++
		}
	}
	if matchingLeases != 1 {
		return errors.New("mission lease binding is ambiguous")
	}
	for function, agentID := range mission.RoleAssignments {
		agent, exists := state.Agents[agentID]
		if !exists || agent.Status != "active" || agent.Function != function {
			return fmt.Errorf("mission assignment %q is not active for function %q", agentID, function)
		}
	}
	if _, exists := state.Missions[mission.ID]; exists {
		return fmt.Errorf("mission %q already exists", mission.ID)
	}
	state.Missions[mission.ID] = mission
	return nil
}

func applyMissionInvalidated(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload MissionInvalidated
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "mission" || event.AggregateID != payload.MissionID {
		return errors.New("mission invalidation aggregate does not match payload")
	}
	mission, exists := state.Missions[payload.MissionID]
	if !exists {
		return fmt.Errorf("mission %q not found", payload.MissionID)
	}
	if !canInvalidateMission(event.Actor, mission) {
		return errors.New("mission invalidation requires an eligible human actor for the mission risk")
	}
	for _, agentID := range mission.RoleAssignments {
		if event.Actor.ID == agentID {
			return errors.New("mission assignment cannot invalidate itself")
		}
	}
	delete(state.Missions, payload.MissionID)
	return nil
}

func applyApprovalRequested(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ApprovalRequested
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	approval := payload.Approval
	if strings.TrimSpace(approval.ID) == "" || strings.TrimSpace(approval.SubjectType) == "" || strings.TrimSpace(approval.SubjectID) == "" || strings.TrimSpace(approval.PayloadSHA256) == "" || strings.TrimSpace(approval.RequesterID) == "" {
		return errors.New("approval id, subject, payload hash, and requester are required")
	}
	if event.AggregateType != "approval" || approval.ID != event.AggregateID {
		return errors.New("approval id does not match aggregate id")
	}
	if approval.RequesterID != event.Actor.ID {
		return errors.New("approval requester does not match event actor")
	}
	if !IsValidRiskLevel(approval.RiskLevel) {
		return errors.New("approval risk level is invalid")
	}
	if _, exists := state.Approvals[approval.ID]; exists {
		return fmt.Errorf("approval %q already exists", approval.ID)
	}
	approval.Status = "requested"
	state.Approvals[approval.ID] = approval
	return nil
}

func applyApprovalDecided(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ApprovalDecided
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "approval" || event.AggregateID != payload.ApprovalID {
		return errors.New("approval decision aggregate does not match payload")
	}
	approval, exists := state.Approvals[payload.ApprovalID]
	if !exists {
		return fmt.Errorf("approval %q not found", payload.ApprovalID)
	}
	if approval.Status != "requested" {
		return fmt.Errorf("approval %q is not pending", approval.ID)
	}
	if approval.PayloadSHA256 != payload.PayloadSHA256 {
		return errors.New("approval payload hash does not match")
	}
	if payload.DeciderID == approval.RequesterID || payload.DeciderID != event.Actor.ID || !canDecideApproval(event.Actor, approval.RiskLevel) {
		return errors.New("approval decision requires an eligible human decider distinct from requester")
	}
	if payload.Decision != "approved" && payload.Decision != "rejected" {
		return errors.New("approval decision must be approved or rejected")
	}
	approval.Status = payload.Decision
	approval.DeciderID = payload.DeciderID
	approval.DecisionReason = payload.DecisionReason
	approval.DecidedAt = payload.DecidedAt
	state.Approvals[approval.ID] = approval
	return nil
}

func applyApprovalInvalidated(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ApprovalInvalidated
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.AggregateType != "approval" || event.AggregateID != payload.ApprovalID {
		return errors.New("approval invalidation aggregate does not match payload")
	}
	approval, exists := state.Approvals[payload.ApprovalID]
	if !exists {
		return fmt.Errorf("approval %q not found", payload.ApprovalID)
	}
	if approval.Status != "requested" {
		return fmt.Errorf("approval %q is not pending", approval.ID)
	}
	if approval.RequesterID == event.Actor.ID || !canDecideApproval(event.Actor, approval.RiskLevel) {
		return errors.New("approval invalidation requires an eligible human decider distinct from requester")
	}
	approval.Status = "invalidated"
	approval.DecisionReason = payload.Reason
	state.Approvals[approval.ID] = approval
	return nil
}

func canDecideApproval(actor Actor, risk string) bool {
	if actor.Kind != ActorHuman {
		return false
	}
	switch risk {
	case "L2":
		return actor.Role == RoleLead || actor.Role == RoleReviewer
	case "L3":
		return actor.Role == RoleOwner
	default:
		return actor.Role == RoleOwner || actor.Role == RoleLead || actor.Role == RoleReviewer
	}
}

func canInvalidateMission(actor Actor, mission MissionEnvelope) bool {
	if actor.Kind != ActorHuman {
		return false
	}
	switch mission.RiskLevel {
	case "L2":
		return actor.Role == RoleLead || actor.Role == RoleReviewer
	case "L3", "L0", "L1":
		return actor.Role == RoleOwner
	default:
		return false
	}
}

func missionApprovalRequired(risk string) bool {
	return risk == "L2" || risk == "L3"
}

func hasApprovedMissionApproval(state ProjectState, mission MissionEnvelope) bool {
	for _, approval := range state.Approvals {
		if approval.Status == "approved" && approval.SubjectType == "mission" && approval.SubjectID == mission.ID && approval.PayloadSHA256 == mission.Hash && approval.RiskLevel == mission.RiskLevel && approval.DeciderID != approval.RequesterID {
			return true
		}
	}
	return false
}

func applyConflictOpened(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ConflictOpened
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	conflict := payload.Conflict
	if strings.TrimSpace(conflict.ID) == "" {
		return errors.New("conflict id is required")
	}
	if _, exists := state.Conflicts[conflict.ID]; exists {
		return fmt.Errorf("conflict %q already exists", conflict.ID)
	}
	conflict.Status = "open"
	state.Conflicts[conflict.ID] = conflict
	return nil
}

func applyConflictResolved(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ConflictResolved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if event.Actor.Kind != ActorHuman || (event.Actor.Role != RoleOwner && event.Actor.Role != RoleLead && event.Actor.Role != RoleReviewer) {
		return errors.New("conflict resolver must be a human owner, lead, or reviewer")
	}
	if payload.ResolverID != event.Actor.ID {
		return fmt.Errorf("conflict resolver id %q does not match event actor %q", payload.ResolverID, event.Actor.ID)
	}
	conflict, exists := state.Conflicts[payload.ConflictID]
	if !exists {
		return fmt.Errorf("conflict %q not found", payload.ConflictID)
	}
	if conflict.Status != "open" {
		return fmt.Errorf("conflict %q status %q requires open", conflict.ID, conflict.Status)
	}
	conflict.Status = "resolved"
	conflict.ResolverID = payload.ResolverID
	conflict.Resolution = payload.Resolution
	conflict.ResolvedAt = payload.ResolvedAt
	state.Conflicts[conflict.ID] = conflict
	return nil
}

func applyContextIssued(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ContextIssued
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	context := payload.Context
	if strings.TrimSpace(context.ID) == "" || strings.TrimSpace(context.TaskID) == "" {
		return errors.New("context id and task id are required")
	}
	if context.GoalVersion != state.Goal.Version {
		return fmt.Errorf("context %q goal version %d does not match %d", context.ID, context.GoalVersion, state.Goal.Version)
	}
	if _, exists := state.Tasks[context.TaskID]; !exists {
		return fmt.Errorf("task %q not found", context.TaskID)
	}
	if _, exists := state.Contexts[context.ID]; exists {
		return fmt.Errorf("context id %q already exists", context.ID)
	}
	if strings.TrimSpace(context.SliceHash) == "" {
		return errors.New("context slice hash is required")
	}
	state.Contexts[context.ID] = context
	return nil
}

func applyContextSuperseded(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ContextSuperseded
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	context, exists := state.Contexts[payload.ContextID]
	if !exists {
		return fmt.Errorf("context %q not found", payload.ContextID)
	}
	if context.Superseded {
		return fmt.Errorf("context %q is already superseded", payload.ContextID)
	}
	context.Superseded = true
	state.Contexts[context.ID] = context
	return nil
}

func applyRequirementPlanned(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RequirementPlanned
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	requirement := payload.Requirement
	if requirement.ID == "" {
		return errors.New("requirement id is required")
	}
	if _, exists := state.Requirements[requirement.ID]; exists {
		return fmt.Errorf("requirement id %q already exists", requirement.ID)
	}
	if requirement.GoalVersion != state.Goal.Version {
		return fmt.Errorf("requirement %q goal version %d does not match %d", requirement.ID, requirement.GoalVersion, state.Goal.Version)
	}

	seenTaskIDs := make(map[string]struct{}, len(payload.Tasks))
	for _, task := range payload.Tasks {
		if task.ID == "" {
			return errors.New("task id is required")
		}
		if _, exists := state.Tasks[task.ID]; exists {
			return fmt.Errorf("task id %q already exists", task.ID)
		}
		if _, exists := seenTaskIDs[task.ID]; exists {
			return fmt.Errorf("task id %q already exists", task.ID)
		}
		seenTaskIDs[task.ID] = struct{}{}
		if task.RequirementID != requirement.ID {
			return fmt.Errorf("task %q references requirement %q, want %q", task.ID, task.RequirementID, requirement.ID)
		}
		if task.GoalVersion != state.Goal.Version {
			return fmt.Errorf("task %q goal version %d does not match %d", task.ID, task.GoalVersion, state.Goal.Version)
		}
	}

	requirement.Status = StatusDraft
	state.Requirements[requirement.ID] = requirement
	for _, task := range payload.Tasks {
		task.Status = StatusDraft
		state.Tasks[task.ID] = task
	}
	return nil
}

func applyRequirementApproved(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RequirementApproved
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	requirement, exists := state.Requirements[payload.RequirementID]
	if !exists {
		return fmt.Errorf("requirement %q not found", payload.RequirementID)
	}
	if requirement.Status != StatusDraft {
		return statusError("requirement", requirement.ID, requirement.Status, StatusDraft)
	}

	requirement.Status = StatusApproved
	state.Requirements[requirement.ID] = requirement
	for id, task := range state.Tasks {
		if task.RequirementID != requirement.ID {
			continue
		}
		task.Status = StatusApproved
		state.Tasks[id] = task
	}
	return nil
}

func applyRunStarted(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RunStarted
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	run := payload.Run
	if run.ID == "" {
		return errors.New("run id is required")
	}
	if _, exists := state.Runs[run.ID]; exists {
		return fmt.Errorf("run id %q already exists", run.ID)
	}
	if run.GoalVersion != state.Goal.Version {
		return fmt.Errorf("run %q goal version %d does not match %d", run.ID, run.GoalVersion, state.Goal.Version)
	}
	if (run.ContextID == "") != (run.ContextHash == "") {
		return errors.New("context id and context hash must be provided together")
	}
	if run.ContextID != "" {
		context, exists := state.Contexts[run.ContextID]
		if !exists {
			return fmt.Errorf("context %q not found", run.ContextID)
		}
		if context.TaskID != run.TaskID {
			return fmt.Errorf("context %q belongs to task %q, not %q", run.ContextID, context.TaskID, run.TaskID)
		}
		if context.Superseded {
			return fmt.Errorf("context %q is superseded", run.ContextID)
		}
		if run.ContextHash != context.SliceHash {
			return fmt.Errorf("run %q context hash does not match context %q", run.ID, run.ContextID)
		}
	}
	task, exists := state.Tasks[run.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", run.TaskID)
	}
	if task.Status != StatusApproved {
		return statusError("task", task.ID, task.Status, StatusApproved)
	}

	run.Status = StatusRunning
	state.Runs[run.ID] = run
	task.Status = StatusRunning
	task.LastRunID = run.ID
	state.Tasks[task.ID] = task
	return nil
}

func applyRunStepStarted(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RunStepStarted
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	step := payload.Step
	if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.RunID) == "" {
		return errors.New("step id and run id are required")
	}
	run, exists := state.Runs[step.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", step.RunID)
	}
	if run.Status != StatusRunning {
		return statusError("run", run.ID, run.Status, StatusRunning)
	}
	if _, exists := state.Steps[step.ID]; exists {
		return fmt.Errorf("step id %q already exists", step.ID)
	}
	step.Status = "started"
	state.Steps[step.ID] = step
	return nil
}

func applyRunStepFinished(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RunStepFinished
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	step, exists := state.Steps[payload.Step.ID]
	if !exists {
		return fmt.Errorf("step %q not found", payload.Step.ID)
	}
	if payload.Step.RunID != "" && payload.Step.RunID != step.RunID {
		return fmt.Errorf("step %q run id does not match", step.ID)
	}
	if step.Status != "started" {
		return fmt.Errorf("step %q status %q, requires %q", step.ID, step.Status, "started")
	}
	run := state.Runs[step.RunID]
	if run.Status != StatusRunning {
		return statusError("run", run.ID, run.Status, StatusRunning)
	}
	if payload.Step.Kind != "" {
		step.Kind = payload.Step.Kind
	}
	if payload.Step.Summary != "" {
		step.Summary = payload.Step.Summary
	}
	if payload.Step.ArtifactRefs != nil {
		step.ArtifactRefs = payload.Step.ArtifactRefs
	}
	step.Status = "finished"
	state.Steps[step.ID] = step
	return nil
}

func applyCheckpointCreated(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload CheckpointCreated
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	checkpoint := payload.Checkpoint
	if strings.TrimSpace(checkpoint.ID) == "" || strings.TrimSpace(checkpoint.RunID) == "" {
		return errors.New("checkpoint id and run id are required")
	}
	run, exists := state.Runs[checkpoint.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", checkpoint.RunID)
	}
	if checkpoint.StepID != "" {
		step, exists := state.Steps[checkpoint.StepID]
		if !exists {
			return fmt.Errorf("step %q not found", checkpoint.StepID)
		}
		if step.RunID != checkpoint.RunID {
			return fmt.Errorf("step %q belongs to run %q, not %q", step.ID, step.RunID, checkpoint.RunID)
		}
	}
	if _, exists := state.Checkpoints[checkpoint.ID]; exists {
		return fmt.Errorf("checkpoint id %q already exists", checkpoint.ID)
	}
	if run.Status != StatusRunning {
		return statusError("run", run.ID, run.Status, StatusRunning)
	}
	if err := validateCurrentRunContext(state, run); err != nil {
		return err
	}
	if strings.TrimSpace(checkpoint.ContextHash) == "" || checkpoint.ContextHash != run.ContextHash {
		return fmt.Errorf("checkpoint %q context hash does not match run %q", checkpoint.ID, checkpoint.RunID)
	}
	if strings.TrimSpace(checkpoint.WorkspaceDigest) == "" || strings.TrimSpace(checkpoint.AdapterCursor) == "" {
		return errors.New("checkpoint workspace digest and adapter cursor are required")
	}
	state.Checkpoints[checkpoint.ID] = checkpoint
	run.AdapterCursor = checkpoint.AdapterCursor
	state.Runs[run.ID] = run
	return nil
}

func applyExecutorEventReceived(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ExecutorEventReceived
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	executorEvent := payload.ExecutorEvent
	if strings.TrimSpace(executorEvent.RunID) == "" || strings.TrimSpace(executorEvent.Cursor) == "" {
		return errors.New("executor event run id and cursor are required")
	}
	run, exists := state.Runs[executorEvent.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", executorEvent.RunID)
	}
	if executorEvent.StepID != "" {
		step, exists := state.Steps[executorEvent.StepID]
		if !exists {
			return fmt.Errorf("step %q not found", executorEvent.StepID)
		}
		if step.RunID != executorEvent.RunID {
			return fmt.Errorf("step %q belongs to run %q, not %q", step.ID, step.RunID, executorEvent.RunID)
		}
	}
	if !validExecutorEventKind(executorEvent.Kind) {
		return fmt.Errorf("unknown executor event kind %q", executorEvent.Kind)
	}
	if exists, err := HasExecutorEvent(state.ExecutorEvents, executorEvent); err != nil {
		return err
	} else if exists {
		return nil
	}
	key := ExecutorEventKey(executorEvent)
	if run.Status != StatusRunning {
		return statusError("run", run.ID, run.Status, StatusRunning)
	}
	state.ExecutorEvents[key] = executorEvent
	switch executorEvent.Kind {
	case "paused":
		run.Status = StatusPaused
	case "failed":
		run.Status = StatusFailed
	case "cancelled":
		run.Status = StatusCancelled
	default:
		return nil
	}
	state.Runs[run.ID] = run
	return nil
}

func applyRunResumed(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RunResumed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	run, exists := state.Runs[payload.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", payload.RunID)
	}
	if run.Status != StatusPaused && run.Status != StatusFailed {
		return fmt.Errorf("run %q status %q cannot resume", run.ID, run.Status)
	}
	if strings.TrimSpace(run.AdapterCursor) == "" || payload.AdapterCursor != run.AdapterCursor {
		return fmt.Errorf("run %q resume cursor does not match latest checkpoint", run.ID)
	}
	if err := validateCurrentRunContext(state, run); err != nil {
		return err
	}
	task, exists := state.Tasks[run.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", run.TaskID)
	}
	if task.Status != StatusRunning || task.LastRunID != run.ID {
		return fmt.Errorf("run %q is not current and running for task %q", run.ID, task.ID)
	}
	run.Status = StatusRunning
	state.Runs[run.ID] = run
	return nil
}

func applyRunFinished(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload RunFinished
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	run, exists := state.Runs[payload.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", payload.RunID)
	}
	if run.Status != StatusRunning {
		return statusError("run", run.ID, run.Status, StatusRunning)
	}
	task, exists := state.Tasks[run.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", run.TaskID)
	}
	if task.Status != StatusRunning {
		return statusError("task", task.ID, task.Status, StatusRunning)
	}

	run.Status = StatusFinished
	run.Result = payload.Result
	state.Runs[run.ID] = run
	task.Status = StatusVerifying
	state.Tasks[task.ID] = task
	return nil
}

func applyEvidenceRecorded(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload EvidenceRecorded
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	evidence := payload.Evidence
	if evidence.ID == "" {
		return errors.New("evidence id is required")
	}
	if evidenceIDExists(state, evidence.ID) {
		return fmt.Errorf("evidence id %q already exists", evidence.ID)
	}
	task, exists := state.Tasks[evidence.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", evidence.TaskID)
	}
	if task.Status != StatusVerifying {
		return statusError("task", task.ID, task.Status, StatusVerifying)
	}

	state.Evidence[task.ID] = append(state.Evidence[task.ID], evidence)
	if evidence.Outcome == "pass" {
		task.Status = StatusVerified
		state.Tasks[task.ID] = task
	}
	return nil
}

func applyEvidenceCandidateRecorded(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload EvidenceCandidateRecorded
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	evidence := payload.Evidence
	if err := validateContextualEvidence(state, evidence); err != nil {
		return err
	}
	if evidenceIDExists(state, evidence.ID) {
		return fmt.Errorf("evidence id %q already exists", evidence.ID)
	}
	evidence.Status = "candidate"
	evidence.Actor = event.Actor
	state.Evidence[evidence.TaskID] = append(state.Evidence[evidence.TaskID], evidence)
	return nil
}

func applyEvidenceVerified(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload EvidenceVerified
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateContextualEvidence(state, payload.Evidence); err != nil {
		return err
	}
	return updateEvidenceStatus(state, payload.Evidence, "verified")
}

func applyEvidenceInvalidated(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload EvidenceInvalidated
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Evidence.ID) == "" {
		return errors.New("evidence id is required")
	}
	return updateEvidenceStatus(state, payload.Evidence, "invalidated")
}

func applyTaskCompleted(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload TaskCompleted
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	task, exists := state.Tasks[payload.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", payload.TaskID)
	}
	if task.Status != StatusVerified {
		return statusError("task", task.ID, task.Status, StatusVerified)
	}
	if task.LastRunID != "" {
		run, exists := state.Runs[task.LastRunID]
		if !exists {
			return fmt.Errorf("last run %q for task %q not found", task.LastRunID, task.ID)
		}
		if run.Status != StatusFinished {
			return statusError("run", run.ID, run.Status, StatusFinished)
		}
	}
	task.Status = StatusCompleted
	state.Tasks[task.ID] = task
	return nil
}

func applyChangesScanned(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ChangesScanned
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	changes := make(map[string]FileChange, len(payload.Changes))
	for _, change := range payload.Changes {
		if strings.TrimSpace(change.Path) == "" {
			return errors.New("file change path is required")
		}
		if _, exists := changes[change.Path]; exists {
			return fmt.Errorf("file change path %q appears more than once", change.Path)
		}
		if strings.TrimSpace(change.Status) == "" {
			return fmt.Errorf("file change %q status is required", change.Path)
		}
		if strings.TrimSpace(change.Baseline) == "" {
			return fmt.Errorf("file change %q baseline is required", change.Path)
		}
		if change.Status == "deleted" {
			if change.SHA256 != "" {
				return fmt.Errorf("deleted file change %q must not have a SHA-256", change.Path)
			}
		} else if strings.TrimSpace(change.SHA256) == "" {
			return fmt.Errorf("file change %q SHA-256 is required", change.Path)
		}
		change.Attributed = false
		if _, exists := state.Attributions[changeAttributionKey(change.Path, change.SHA256)]; exists {
			change.Attributed = true
		}
		changes[change.Path] = change
	}
	state.Changes = changes
	return nil
}

func applyChangeAttributed(state *ProjectState, event Event) error {
	if err := requireInitialized(state); err != nil {
		return err
	}
	var payload ChangeAttributed
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if strings.TrimSpace(payload.Path) == "" || strings.TrimSpace(payload.TaskID) == "" {
		return errors.New("change attribution path and task id are required")
	}
	change, exists := state.Changes[payload.Path]
	if !exists || change.SHA256 != payload.SHA256 {
		return fmt.Errorf("current file change %q with SHA-256 %q not found", payload.Path, payload.SHA256)
	}
	if payload.SHA256 == "" && change.Status != "deleted" {
		return fmt.Errorf("file change %q SHA-256 is required", payload.Path)
	}
	if payload.TaskID == "external-manual" {
		if strings.TrimSpace(payload.Note) == "" {
			return errors.New("external-manual attribution requires a note")
		}
	} else if _, exists := state.Tasks[payload.TaskID]; !exists {
		return fmt.Errorf("task %q not found", payload.TaskID)
	}
	attribution := ChangeAttribution{Path: payload.Path, SHA256: payload.SHA256, TaskID: payload.TaskID, Note: payload.Note}
	if state.Attributions == nil {
		state.Attributions = make(map[string]ChangeAttribution)
	}
	state.Attributions[changeAttributionKey(payload.Path, payload.SHA256)] = attribution
	change.Attributed = true
	state.Changes[payload.Path] = change
	return nil
}

func changeAttributionKey(path, sha256 string) string {
	return path + "\x00" + sha256
}

func ExecutorEventKey(event ExecutorEvent) string {
	if sourceEventID := strings.TrimSpace(event.SourceEventID); sourceEventID != "" {
		return event.RunID + "\x00source:" + sourceEventID
	}
	return event.RunID + "\x00" + event.Cursor
}

// HasExecutorEvent recognizes both the current source-event key and the legacy cursor key.
func HasExecutorEvent(events map[string]ExecutorEvent, candidate ExecutorEvent) (bool, error) {
	candidateID := executorEventSourceID(candidate)
	foundKeys := make(map[string]struct{})
	for key, event := range events {
		if event.RunID != candidate.RunID {
			continue
		}
		eventID := executorEventSourceID(event)
		if event.Cursor == candidate.Cursor {
			if candidateID != "" && eventID != "" && candidateID != eventID {
				return false, errors.New("executor event cursor maps to a different source event id")
			}
			foundKeys[key] = struct{}{}
			continue
		}
		if candidateID != "" && candidateID == eventID {
			foundKeys[key] = struct{}{}
		}
	}
	if len(foundKeys) > 1 {
		return false, errors.New("executor event source and legacy cursor keys conflict")
	}
	return len(foundKeys) == 1, nil
}

func executorEventSourceID(event ExecutorEvent) string {
	if sourceEventID := strings.TrimSpace(event.SourceEventID); sourceEventID != "" {
		return sourceEventID
	}
	index := strings.LastIndex(event.Cursor, ":")
	if index < 0 {
		return ""
	}
	legacyID := strings.TrimSpace(event.Cursor[index+1:])
	if strings.HasPrefix(legacyID, "$") {
		return legacyID
	}
	return ""
}

func decodePayload(event Event, destination any) error {
	if err := json.Unmarshal(event.Payload, destination); err != nil {
		return fmt.Errorf("invalid %s payload: %w", event.Type, err)
	}
	return nil
}

func requireInitialized(state *ProjectState) error {
	if state.ProjectID == "" {
		return errors.New("project is not initialized")
	}
	return nil
}

func hasNonEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func statusError(aggregateType, aggregateID string, current, required Status) error {
	return fmt.Errorf("%s %q status %q, requires %q", aggregateType, aggregateID, current, required)
}

func evidenceIDExists(state *ProjectState, evidenceID string) bool {
	for _, records := range state.Evidence {
		for _, evidence := range records {
			if evidence.ID == evidenceID {
				return true
			}
		}
	}
	return false
}

func validateContextualEvidence(state *ProjectState, evidence Evidence) error {
	if strings.TrimSpace(evidence.ID) == "" || strings.TrimSpace(evidence.TaskID) == "" || strings.TrimSpace(evidence.RunID) == "" || strings.TrimSpace(evidence.ContextID) == "" {
		return errors.New("contextual evidence id, task id, run id, and context id are required")
	}
	if evidence.GoalVersion != state.Goal.Version {
		return fmt.Errorf("evidence %q goal version %d does not match %d", evidence.ID, evidence.GoalVersion, state.Goal.Version)
	}
	task, exists := state.Tasks[evidence.TaskID]
	if !exists {
		return fmt.Errorf("task %q not found", evidence.TaskID)
	}
	run, exists := state.Runs[evidence.RunID]
	if !exists {
		return fmt.Errorf("run %q not found", evidence.RunID)
	}
	if run.TaskID != evidence.TaskID {
		return fmt.Errorf("evidence %q task does not match run %q", evidence.ID, evidence.RunID)
	}
	if run.Status != StatusFinished {
		return statusError("run", run.ID, run.Status, StatusFinished)
	}
	if task.Status != StatusVerifying {
		return statusError("task", task.ID, task.Status, StatusVerifying)
	}
	if task.LastRunID != run.ID {
		return fmt.Errorf("run %q is not current for task %q", run.ID, task.ID)
	}
	context, exists := state.Contexts[evidence.ContextID]
	if !exists {
		return fmt.Errorf("context %q not found", evidence.ContextID)
	}
	if context.TaskID != evidence.TaskID {
		return fmt.Errorf("context %q belongs to task %q, not %q", evidence.ContextID, context.TaskID, evidence.TaskID)
	}
	if context.GoalVersion != evidence.GoalVersion {
		return fmt.Errorf("evidence %q goal version does not match context %q", evidence.ID, evidence.ContextID)
	}
	if run.ContextID != evidence.ContextID {
		return fmt.Errorf("evidence %q context id does not match run %q", evidence.ID, evidence.RunID)
	}
	if run.ContextHash != context.SliceHash {
		return fmt.Errorf("run %q context hash does not match context %q", evidence.RunID, evidence.ContextID)
	}
	return nil
}

func validateCurrentRunContext(state *ProjectState, run Run) error {
	if strings.TrimSpace(run.ContextID) == "" || strings.TrimSpace(run.ContextHash) == "" {
		return fmt.Errorf("run %q requires a current context", run.ID)
	}
	context, exists := state.Contexts[run.ContextID]
	if !exists {
		return fmt.Errorf("context %q not found", run.ContextID)
	}
	if context.TaskID != run.TaskID || context.GoalVersion != run.GoalVersion || context.Superseded || context.SliceHash != run.ContextHash {
		return fmt.Errorf("run %q context is not current", run.ID)
	}
	return nil
}

func validExecutorEventKind(kind string) bool {
	switch kind {
	case "stdout", "stderr", "progress", "notice", "paused", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func updateEvidenceStatus(state *ProjectState, evidence Evidence, status string) error {
	for taskID, records := range state.Evidence {
		for index, record := range records {
			if record.ID != evidence.ID {
				continue
			}
			if evidence.TaskID != "" && evidence.TaskID != taskID {
				return fmt.Errorf("evidence %q task id does not match", evidence.ID)
			}
			if evidence.RunID != "" && evidence.RunID != record.RunID {
				return fmt.Errorf("evidence %q run id does not match", evidence.ID)
			}
			if evidence.ContextID != "" && evidence.ContextID != record.ContextID {
				return fmt.Errorf("evidence %q context id does not match", evidence.ID)
			}
			if status == "verified" && record.Status != "candidate" {
				return fmt.Errorf("evidence %q status %q, requires %q", evidence.ID, record.Status, "candidate")
			}
			if evidence.URI != "" {
				record.URI = evidence.URI
			}
			if evidence.SHA256 != "" {
				record.SHA256 = evidence.SHA256
			}
			if evidence.Command != "" {
				record.Command = evidence.Command
			}
			if evidence.Baseline != "" {
				record.Baseline = evidence.Baseline
			}
			if evidence.Source != "" {
				record.Source = evidence.Source
			}
			if evidence.Checks != nil {
				record.Checks = evidence.Checks
			}
			record.Status = status
			records[index] = record
			state.Evidence[taskID] = records
			recomputeTaskVerification(state, taskID)
			return nil
		}
	}
	return fmt.Errorf("evidence %q not found", evidence.ID)
}

func recomputeTaskVerification(state *ProjectState, taskID string) {
	task, exists := state.Tasks[taskID]
	if !exists {
		return
	}
	for _, evidence := range state.Evidence[taskID] {
		if evidence.Status == "verified" {
			task.Status = StatusVerified
			state.Tasks[taskID] = task
			return
		}
	}
	if task.Status == StatusVerified || task.Status == StatusCompleted {
		task.Status = StatusVerifying
		state.Tasks[taskID] = task
	}
}

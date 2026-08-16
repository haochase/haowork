package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/mission"
	"github.com/haochase/haowork/internal/model"
)

// PrepareMission returns a canonical envelope that can be approved before it is issued.
func (s *Service) PrepareMission(ctx context.Context, input IssueMissionInput) (mission.Envelope, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, _, err := s.snapshot(ctx)
	if err != nil {
		return mission.Envelope{}, err
	}
	envelope, _, err := s.prepareMission(state, input, false)
	return envelope, err
}

func (s *Service) IssueMission(ctx context.Context, input IssueMissionInput) (mission.Envelope, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return mission.Envelope{}, err
	}
	envelope, issuedAt, err := s.prepareMission(state, input, true)
	if err != nil {
		return mission.Envelope{}, err
	}
	if envelope.RiskLevel == "L3" && (input.Actor.Kind != model.ActorHuman || input.Actor.Role != model.RoleOwner) {
		return mission.Envelope{}, ErrApprovalRequired
	}
	if missionApprovalRequired(envelope.RiskLevel) && !hasApprovedMissionRequest(state, envelope) {
		return mission.Envelope{}, ErrApprovalRequired
	}
	occurredAt := issuedAt
	if missionApprovalRequired(envelope.RiskLevel) {
		occurredAt = s.clock.Now()
		if occurredAt.IsZero() {
			return mission.Envelope{}, wrapOperational(errors.New("clock returned zero time"))
		}
	}
	payload, err := json.Marshal(model.MissionIssued{Envelope: model.MissionEnvelope(envelope)})
	if err != nil {
		return mission.Envelope{}, err
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return mission.Envelope{}, wrapOperational(err)
	}
	event := model.Event{
		ID: eventID, Type: "mission.issued", ProjectID: s.projectID, GoalVersion: state.Goal.Version,
		AggregateType: "mission", AggregateID: envelope.ID, Actor: input.Actor, OccurredAt: occurredAt.UTC(), Payload: payload,
	}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return mission.Envelope{}, err
	}
	return envelope, nil
}

func (s *Service) prepareMission(state model.ProjectState, input IssueMissionInput, requireStableApprovalFields bool) (mission.Envelope, time.Time, error) {
	if err := validateActor(input.Actor); err != nil {
		return mission.Envelope{}, time.Time{}, err
	}
	if !model.IsValidRiskLevel(strings.TrimSpace(input.RiskLevel)) {
		return mission.Envelope{}, time.Time{}, errors.New("mission risk level is invalid")
	}
	contextSlice, exists := state.Contexts[input.ContextID]
	if !exists || contextSlice.Superseded || contextSlice.GoalVersion != state.Goal.Version {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: context %q is not current", ErrConflict, input.ContextID)
	}
	if len(input.TaskIDs) != 1 {
		return mission.Envelope{}, time.Time{}, errors.New("mission requires exactly one task binding")
	}
	taskID := input.TaskIDs[0]
	task, exists := state.Tasks[taskID]
	if !exists || task.Status != model.StatusApproved || task.GoalVersion != state.Goal.Version {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: task %q is not approved at the current goal version", ErrConflict, taskID)
	}
	if contextSlice.TaskID != taskID {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: context %q does not belong to an issued task", ErrConflict, input.ContextID)
	}
	if err := validateMissionAssignments(state, input.Assignments); err != nil {
		return mission.Envelope{}, time.Time{}, err
	}
	lease, foundLease, err := currentMissionLease(state, input.ContextID, input.EnvironmentID, taskID, input.Assignments[model.FunctionBuild])
	if err != nil {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if !foundLease {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: no active lease matches mission context, environment, and build assignment", ErrConflict)
	}
	if !model.MissionCapabilitiesWithinLease(lease, input.AllowedScopes, input.Skills) {
		return mission.Envelope{}, time.Time{}, fmt.Errorf("%w: mission capabilities exceed the active lease", ErrConflict)
	}
	missionID := strings.TrimSpace(input.MissionID)
	if requireStableApprovalFields && missionApprovalRequired(strings.TrimSpace(input.RiskLevel)) && (missionID == "" || input.IssuedAt.IsZero()) {
		return mission.Envelope{}, time.Time{}, ErrApprovalRequired
	}
	if missionID == "" {
		missionID, err = s.ids.New("MSN")
		if err != nil {
			return mission.Envelope{}, time.Time{}, wrapOperational(err)
		}
	}
	issuedAt := input.IssuedAt
	if issuedAt.IsZero() {
		issuedAt = s.clock.Now()
		if issuedAt.IsZero() {
			return mission.Envelope{}, time.Time{}, wrapOperational(errors.New("clock returned zero time"))
		}
	}
	envelope, err := mission.Build(mission.BuildInput{
		ID: missionID, ProjectID: s.projectID, Context: contextSlice, Lease: lease, GoalVersion: state.Goal.Version,
		TaskIDs: input.TaskIDs, CompletionCriteria: input.CompletionCriteria, AllowedScopes: input.AllowedScopes,
		Skills: input.Skills, Assignments: input.Assignments, RiskLevel: input.RiskLevel, EnvironmentID: input.EnvironmentID,
		PolicyVersion: input.PolicyVersion, IssuedAt: issuedAt, Deadline: input.Deadline,
	})
	if err != nil {
		return mission.Envelope{}, time.Time{}, err
	}
	return envelope, issuedAt.UTC(), nil
}

func (s *Service) RequestApproval(ctx context.Context, subjectType, subjectID, payloadSHA256, risk string, actor model.Actor) (model.ApprovalRequest, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.ApprovalRequest{}, err
	}
	if strings.TrimSpace(subjectType) == "" || strings.TrimSpace(subjectID) == "" || strings.TrimSpace(payloadSHA256) == "" || strings.TrimSpace(risk) == "" {
		return model.ApprovalRequest{}, errors.New("approval subject, payload hash, and risk are required")
	}
	if !model.IsValidRiskLevel(strings.TrimSpace(risk)) {
		return model.ApprovalRequest{}, errors.New("approval risk level is invalid")
	}
	id, err := s.ids.New("APR")
	if err != nil {
		return model.ApprovalRequest{}, wrapOperational(err)
	}
	now := s.clock.Now()
	if now.IsZero() {
		return model.ApprovalRequest{}, wrapOperational(errors.New("clock returned zero time"))
	}
	approval := model.ApprovalRequest{ID: id, SubjectType: strings.TrimSpace(subjectType), SubjectID: strings.TrimSpace(subjectID), PayloadSHA256: strings.TrimSpace(payloadSHA256), RiskLevel: strings.TrimSpace(risk), RequesterID: actor.ID, Status: "requested", RequestedAt: now.UTC()}
	payload, err := json.Marshal(model.ApprovalRequested{Approval: approval})
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.ApprovalRequest{}, wrapOperational(err)
	}
	event := model.Event{ID: eventID, Type: "approval.requested", ProjectID: s.projectID, GoalVersion: state.Goal.Version, AggregateType: "approval", AggregateID: id, Actor: actor, OccurredAt: now.UTC(), Payload: payload}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.ApprovalRequest{}, err
	}
	return approval, nil
}

func (s *Service) DecideApproval(ctx context.Context, approvalID, payloadSHA256, decision, reason string, actor model.Actor) (model.ApprovalRequest, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if actor.Kind != model.ActorHuman {
		return model.ApprovalRequest{}, ErrApprovalRequired
	}
	approval, exists := state.Approvals[approvalID]
	if !exists {
		return model.ApprovalRequest{}, fmt.Errorf("%w: approval %q not found", ErrConflict, approvalID)
	}
	if approval.RequesterID == actor.ID || !canDecideMissionApproval(actor, approval.RiskLevel) {
		return model.ApprovalRequest{}, ErrApprovalRequired
	}
	if approval.Status != "requested" {
		return model.ApprovalRequest{}, fmt.Errorf("%w: approval %q is not pending", ErrConflict, approvalID)
	}
	now := s.clock.Now()
	if now.IsZero() {
		return model.ApprovalRequest{}, wrapOperational(errors.New("clock returned zero time"))
	}
	if strings.TrimSpace(payloadSHA256) != approval.PayloadSHA256 {
		payload, err := json.Marshal(model.ApprovalInvalidated{ApprovalID: approval.ID, Reason: "payload hash changed"})
		if err != nil {
			return model.ApprovalRequest{}, err
		}
		eventID, err := s.ids.New("EVT")
		if err != nil {
			return model.ApprovalRequest{}, wrapOperational(err)
		}
		event := model.Event{ID: eventID, Type: "approval.invalidated", ProjectID: s.projectID, GoalVersion: state.Goal.Version, AggregateType: "approval", AggregateID: approval.ID, Actor: actor, OccurredAt: now.UTC(), Payload: payload}
		if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
			return model.ApprovalRequest{}, err
		}
		return model.ApprovalRequest{}, fmt.Errorf("%w: approval payload hash changed", ErrConflict)
	}
	if decision != "approved" && decision != "rejected" {
		return model.ApprovalRequest{}, errors.New("approval decision must be approved or rejected")
	}
	payload, err := json.Marshal(model.ApprovalDecided{ApprovalID: approval.ID, PayloadSHA256: approval.PayloadSHA256, Decision: decision, DecisionReason: strings.TrimSpace(reason), DeciderID: actor.ID, DecidedAt: now.UTC()})
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	eventID, err := s.ids.New("EVT")
	if err != nil {
		return model.ApprovalRequest{}, wrapOperational(err)
	}
	event := model.Event{ID: eventID, Type: "approval.decided", ProjectID: s.projectID, GoalVersion: state.Goal.Version, AggregateType: "approval", AggregateID: approval.ID, Actor: actor, OccurredAt: now.UTC(), Payload: payload}
	if err := s.appendPreparedEvent(ctx, eventCount, event); err != nil {
		return model.ApprovalRequest{}, err
	}
	approval.Status = decision
	approval.DeciderID = actor.ID
	approval.DecisionReason = strings.TrimSpace(reason)
	approval.DecidedAt = now.UTC()
	return approval, nil
}

func currentMissionLease(state model.ProjectState, contextID, environmentID, taskID, buildAgentID string) (model.Lease, bool, error) {
	var match model.Lease
	found := false
	for _, lease := range state.Leases {
		if !model.IsActiveMissionLeaseForBuildAssignment(lease, state.Goal.Version, contextID, environmentID, taskID, buildAgentID) {
			continue
		}
		if found {
			return model.Lease{}, false, errors.New("active mission lease binding is ambiguous")
		}
		match = lease
		found = true
	}
	return match, found, nil
}

func validateMissionAssignments(state model.ProjectState, assignments map[model.AgentFunction]string) error {
	for function, agentID := range assignments {
		agent, exists := state.Agents[agentID]
		if !exists || agent.Status != "active" || agent.Function != function {
			return fmt.Errorf("%w: logical agent %q is not active for function %q", ErrConflict, agentID, function)
		}
	}
	return nil
}

func missionApprovalRequired(risk string) bool {
	return risk == "L2" || risk == "L3"
}

func hasApprovedMissionRequest(state model.ProjectState, envelope mission.Envelope) bool {
	for _, approval := range state.Approvals {
		if approval.Status == "approved" && approval.SubjectType == "mission" && approval.SubjectID == envelope.ID && approval.PayloadSHA256 == envelope.Hash && approval.RiskLevel == envelope.RiskLevel && approval.DeciderID != approval.RequesterID {
			return true
		}
	}
	return false
}

func canDecideMissionApproval(actor model.Actor, risk string) bool {
	if actor.Kind != model.ActorHuman {
		return false
	}
	switch risk {
	case "L2":
		return actor.Role == model.RoleLead || actor.Role == model.RoleReviewer
	case "L3":
		return actor.Role == model.RoleOwner
	default:
		return actor.Role == model.RoleOwner || actor.Role == model.RoleLead || actor.Role == model.RoleReviewer
	}
}

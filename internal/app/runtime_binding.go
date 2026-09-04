package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

// BootstrapRuntimeTopology atomically registers stable logical identities and
// their first environment-local runtime bindings under explicit owner control.
func (s *Service) BootstrapRuntimeTopology(ctx context.Context, agents []model.LogicalAgent, bindings []model.RuntimeBinding, actor model.Actor) ([]model.RuntimeBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if actor.Kind != model.ActorHuman || actor.Role != model.RoleOwner {
		return nil, ErrApprovalRequired
	}
	if len(agents) == 0 || len(agents) != len(bindings) {
		return nil, errors.New("logical agents and runtime bindings must be non-empty and aligned")
	}

	prepared := make([]model.Event, 0, len(agents)*2)
	persisted := make([]model.RuntimeBinding, 0, len(bindings))
	seen := make(map[string]struct{}, len(agents))
	for index := range agents {
		agent := agents[index]
		agent.ID = strings.TrimSpace(agent.ID)
		agent.Status = "active"
		binding := bindings[index]
		binding.LogicalActorID = strings.TrimSpace(binding.LogicalActorID)
		binding.EnvironmentID = strings.TrimSpace(binding.EnvironmentID)
		binding.AgentTeamsInstanceID = strings.TrimSpace(binding.AgentTeamsInstanceID)
		binding.RuntimePrincipalID = strings.TrimSpace(binding.RuntimePrincipalID)
		binding.HumanPrincipalID = strings.TrimSpace(binding.HumanPrincipalID)
		binding.LeaderRoomID = strings.TrimSpace(binding.LeaderRoomID)
		binding.TeamRoomID = strings.TrimSpace(binding.TeamRoomID)
		if agent.ID == "" || agent.SubjectKind != model.ActorAgent || agent.GovernanceRole != model.RoleAgent || !bootstrapAgentFunction(agent.Function) {
			return nil, fmt.Errorf("%w: logical Agent bootstrap identity is invalid", ErrConflict)
		}
		if binding.LogicalActorID != agent.ID || binding.EnvironmentID == "" || binding.AgentTeamsInstanceID == "" || binding.RuntimePrincipalID == "" {
			return nil, fmt.Errorf("%w: bootstrap runtime binding is invalid", ErrConflict)
		}
		if _, exists := seen[agent.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate logical Agent %q", ErrConflict, agent.ID)
		}
		seen[agent.ID] = struct{}{}

		if existing, exists := state.Agents[agent.ID]; exists {
			if existing.SubjectKind != agent.SubjectKind || existing.GovernanceRole != agent.GovernanceRole || existing.Function != agent.Function || existing.Status != "active" {
				return nil, fmt.Errorf("%w: logical Agent %q differs from existing identity", ErrConflict, agent.ID)
			}
		} else {
			payload, err := json.Marshal(model.AgentIdentityRegistered{Agent: agent})
			if err != nil {
				return nil, err
			}
			eventID, err := s.ids.New("EVT")
			if err != nil {
				return nil, wrapOperational(err)
			}
			event, err := s.event(eventID, "agent.identity.registered", "agent", agent.ID, actor, payload)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, event)
		}

		history := state.RuntimeBindings[agent.ID]
		if len(history) > 0 {
			latest := history[len(history)-1]
			if !sameActiveRuntimeBinding(latest, binding) {
				return nil, fmt.Errorf("%w: logical Agent %q already has another runtime binding", ErrConflict, agent.ID)
			}
			persisted = append(persisted, latest)
			continue
		}
		binding.Revision = 1
		binding.Status = "active"
		payload, err := json.Marshal(model.RuntimeBound{Binding: binding})
		if err != nil {
			return nil, err
		}
		eventID, err := s.ids.New("EVT")
		if err != nil {
			return nil, wrapOperational(err)
		}
		event, err := s.event(eventID, "agent.runtime.bound", "agent", agent.ID, actor, payload)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, event)
		persisted = append(persisted, binding)
	}
	if len(prepared) > 0 {
		if err := s.appendPreparedEvents(ctx, eventCount, prepared); err != nil {
			return nil, err
		}
	}
	return persisted, nil
}

func bootstrapAgentFunction(value model.AgentFunction) bool {
	switch value {
	case model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify:
		return true
	default:
		return false
	}
}

// BindRuntimeTopology atomically records the control plane's current logical-agent bindings.
func (s *Service) BindRuntimeTopology(ctx context.Context, bindings []model.RuntimeBinding, actor model.Actor) ([]model.RuntimeBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if actor.Kind != model.ActorHuman || actor.Role != model.RoleOwner {
		return nil, ErrApprovalRequired
	}
	if len(bindings) == 0 {
		return nil, errors.New("runtime bindings are required")
	}

	normalized := make([]model.RuntimeBinding, 0, len(bindings))
	existing := make([]model.RuntimeBinding, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	exactRetry := true
	for _, binding := range bindings {
		binding.LogicalActorID = strings.TrimSpace(binding.LogicalActorID)
		binding.EnvironmentID = strings.TrimSpace(binding.EnvironmentID)
		binding.AgentTeamsInstanceID = strings.TrimSpace(binding.AgentTeamsInstanceID)
		binding.RuntimePrincipalID = strings.TrimSpace(binding.RuntimePrincipalID)
		binding.HumanPrincipalID = strings.TrimSpace(binding.HumanPrincipalID)
		binding.LeaderRoomID = strings.TrimSpace(binding.LeaderRoomID)
		binding.TeamRoomID = strings.TrimSpace(binding.TeamRoomID)
		if binding.LogicalActorID == "" || binding.EnvironmentID == "" || binding.AgentTeamsInstanceID == "" || binding.RuntimePrincipalID == "" {
			return nil, fmt.Errorf("%w: runtime binding identity, environment, instance, and principal are required", ErrConflict)
		}
		if _, exists := seen[binding.LogicalActorID]; exists {
			return nil, fmt.Errorf("%w: duplicate runtime binding for %q", ErrConflict, binding.LogicalActorID)
		}
		seen[binding.LogicalActorID] = struct{}{}
		agent, exists := state.Agents[binding.LogicalActorID]
		if !exists || agent.Status != "active" {
			return nil, fmt.Errorf("%w: logical agent %q is not active", ErrConflict, binding.LogicalActorID)
		}
		normalized = append(normalized, binding)
		history := state.RuntimeBindings[binding.LogicalActorID]
		if len(history) == 0 || !sameActiveRuntimeBinding(history[len(history)-1], binding) {
			exactRetry = false
			continue
		}
		existing = append(existing, history[len(history)-1])
	}
	if exactRetry {
		return existing, nil
	}

	prepared := make([]model.Event, 0, len(normalized))
	persisted := make([]model.RuntimeBinding, 0, len(normalized))
	for _, binding := range normalized {
		binding.Revision = len(state.RuntimeBindings[binding.LogicalActorID]) + 1
		binding.Status = "active"
		payload, err := json.Marshal(model.RuntimeBound{Binding: binding})
		if err != nil {
			return nil, err
		}
		eventID, err := s.ids.New("EVT")
		if err != nil {
			return nil, wrapOperational(err)
		}
		event, err := s.event(eventID, "agent.runtime.bound", "agent", binding.LogicalActorID, actor, payload)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, event)
		persisted = append(persisted, binding)
	}
	if err := s.appendPreparedEvents(ctx, eventCount, prepared); err != nil {
		return nil, err
	}
	return persisted, nil
}

func sameActiveRuntimeBinding(existing, candidate model.RuntimeBinding) bool {
	return existing.Status == "active" &&
		existing.LogicalActorID == candidate.LogicalActorID &&
		existing.EnvironmentID == candidate.EnvironmentID &&
		existing.AgentTeamsInstanceID == candidate.AgentTeamsInstanceID &&
		existing.RuntimePrincipalID == candidate.RuntimePrincipalID &&
		existing.HumanPrincipalID == candidate.HumanPrincipalID &&
		existing.LeaderRoomID == candidate.LeaderRoomID &&
		existing.TeamRoomID == candidate.TeamRoomID
}

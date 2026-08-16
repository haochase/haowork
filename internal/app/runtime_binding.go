package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

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

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/contextslice"
	"github.com/haochase/haowork/internal/model"
)

type ContextBuildInput struct {
	TaskID, SupersedesID, Reason       string
	Sources, AllowedPaths, DeniedPaths []string
	Actor                              model.Actor
}

// ConfigureContextBuilder overrides the root-bound builder for controlled tests and integrations.
func (s *Service) ConfigureContextBuilder(builder contextslice.ContextBuilder) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.contextBuilder = builder
}

func (s *Service) BuildContext(ctx context.Context, input ContextBuildInput) (model.ContextSlice, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	state, eventCount, err := s.snapshot(ctx)
	if err != nil {
		return model.ContextSlice{}, err
	}
	if err := validateActor(input.Actor); err != nil {
		return model.ContextSlice{}, err
	}
	input.TaskID = strings.TrimSpace(input.TaskID)
	task, exists := state.Tasks[input.TaskID]
	if !exists {
		return model.ContextSlice{}, fmt.Errorf("%w: task %q not found", ErrConflict, input.TaskID)
	}
	if task.GoalVersion != state.Goal.Version {
		return model.ContextSlice{}, fmt.Errorf("%w: task %q goal version is stale", ErrConflict, input.TaskID)
	}
	if task.Status != model.StatusApproved {
		return model.ContextSlice{}, ErrApprovalRequired
	}
	if input.SupersedesID != "" {
		previous, exists := state.Contexts[input.SupersedesID]
		if !exists || previous.TaskID != input.TaskID || previous.Superseded {
			return model.ContextSlice{}, fmt.Errorf("%w: context %q cannot be superseded", ErrConflict, input.SupersedesID)
		}
	}

	builder, err := s.builderFor(state)
	if err != nil {
		return model.ContextSlice{}, err
	}
	slice, err := builder.Build(ctx, contextslice.ContextRequest{
		TaskID: input.TaskID, SupersedesID: input.SupersedesID, Reason: input.Reason,
		Sources: input.Sources, AllowedPaths: input.AllowedPaths, DeniedPaths: input.DeniedPaths, Actor: input.Actor,
	})
	if err != nil {
		return model.ContextSlice{}, fmt.Errorf("%w: %v", ErrGateFailed, err)
	}
	if slice.TaskID != input.TaskID || slice.GoalVersion != state.Goal.Version || strings.TrimSpace(slice.ID) == "" {
		return model.ContextSlice{}, fmt.Errorf("%w: context builder returned an invalid slice", ErrGateFailed)
	}

	payload, err := json.Marshal(model.ContextIssued{Context: slice})
	if err != nil {
		return model.ContextSlice{}, err
	}
	issuedID, err := s.ids.New("EVT")
	if err != nil {
		return model.ContextSlice{}, wrapOperational(err)
	}
	issued, err := s.event(issuedID, "context.issued", "context", slice.ID, input.Actor, payload)
	if err != nil {
		return model.ContextSlice{}, err
	}
	if slice.SupersedesID == "" {
		if err := s.appendPreparedEvent(ctx, eventCount, issued); err != nil {
			return model.ContextSlice{}, err
		}
	} else {
		supersededPayload, err := json.Marshal(model.ContextSuperseded{ContextID: slice.SupersedesID})
		if err != nil {
			return model.ContextSlice{}, err
		}
		supersededID, err := s.ids.New("EVT")
		if err != nil {
			return model.ContextSlice{}, wrapOperational(err)
		}
		superseded, err := s.event(supersededID, "context.superseded", "context", slice.SupersedesID, input.Actor, supersededPayload)
		if err != nil {
			return model.ContextSlice{}, err
		}
		if err := s.appendPreparedEvents(ctx, eventCount, []model.Event{issued, superseded}); err != nil {
			return model.ContextSlice{}, err
		}
	}
	return s.GetContext(ctx, slice.ID)
}

func (s *Service) GetContext(ctx context.Context, contextID string) (model.ContextSlice, error) {
	state, _, err := s.snapshot(ctx)
	if err != nil {
		return model.ContextSlice{}, err
	}
	slice, exists := state.Contexts[strings.TrimSpace(contextID)]
	if !exists {
		return model.ContextSlice{}, fmt.Errorf("%w: context %q not found", ErrConflict, contextID)
	}
	return slice, nil
}

func (s *Service) builderFor(state model.ProjectState) (contextslice.ContextBuilder, error) {
	if s.contextBuilder != nil {
		return s.contextBuilder, nil
	}
	if s.contextReaderErr != nil {
		return nil, wrapOperational(fmt.Errorf("configure context source reader: %w", s.contextReaderErr))
	}
	if s.contextReader == nil {
		return nil, errors.New("context builder is not configured")
	}
	return contextslice.NewBuilder(s.contextReader, state, s.ids), nil
}

package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/haochase/haowork/internal/model"
)

type AgentTeamsTransport interface {
	Start(context.Context, AgentTeamsStartRequest) (AgentTeamsSession, error)
}

type AgentTeamsStartRequest struct {
	RunID, TaskID, StepID               string
	MissionID, WorkItemID               string
	GoalVersion                         int
	ContextID, ContextHash              string
	Cursor                              string
	Actor                               model.Actor
	LogicalActorID                      string
	RuntimePrincipalID                  string
	RuntimeBindingRevision              int
	AgentFunction                       model.AgentFunction
	EnvironmentID, AgentTeamsInstanceID string
}

type AgentTeamsEvent struct {
	RunID, StepID, Kind, Cursor, SourceEventID, AdapterCursor, Summary string
	ActorID, ActorRole                                                 string
	Artifacts                                                          []model.ArtifactRef
	WorkspaceDigest                                                    string
}

type AgentTeamsSession interface {
	Events(context.Context, string) <-chan AgentTeamsEvent
	Cancel(context.Context) error
}

// BoundAgentTeamsSession optionally exposes the request after a governed
// transport has persisted control-plane-derived runtime binding revisions.
type BoundAgentTeamsSession interface {
	AgentTeamsSession
	BoundRequest() AgentTeamsStartRequest
}

type AgentTeamsAdapter struct {
	transport AgentTeamsTransport
	governed  bool
}

func NewAgentTeamsAdapter(transport AgentTeamsTransport) *AgentTeamsAdapter {
	return &AgentTeamsAdapter{transport: transport}
}

// NewGovernedAgentTeamsAdapter requires app.Service to bind a current Mission Manager runtime before Start.
func NewGovernedAgentTeamsAdapter(transport AgentTeamsTransport) *AgentTeamsAdapter {
	return &AgentTeamsAdapter{transport: transport, governed: true}
}

func (a *AgentTeamsAdapter) RequiresMissionBinding() bool { return a.governed }

func (a *AgentTeamsAdapter) Start(ctx context.Context, request ExecutionRequest) (ExecutionHandle, error) {
	if a == nil || a.transport == nil {
		return nil, fmt.Errorf("%w: AgentTeams transport is not configured", ErrInvalidExecutionRequest)
	}
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if a.governed {
		if err := validateAgentTeamsRequest(request); err != nil {
			return nil, err
		}
	}
	session, err := a.transport.Start(ctx, AgentTeamsStartRequest{
		RunID: request.RunID, TaskID: request.TaskID, StepID: request.StepID, GoalVersion: request.GoalVersion,
		MissionID: request.MissionID, WorkItemID: request.WorkItemID, ContextID: request.ContextID, ContextHash: request.ContextHash, Cursor: request.Cursor, Actor: request.Actor,
		LogicalActorID: request.LogicalActorID, RuntimePrincipalID: request.RuntimePrincipalID, RuntimeBindingRevision: request.RuntimeBindingRevision, AgentFunction: request.AgentFunction, EnvironmentID: request.EnvironmentID, AgentTeamsInstanceID: request.AgentTeamsInstanceID,
	})
	if err != nil {
		return nil, err
	}
	return &agentTeamsHandle{session: session, request: request}, nil
}

func validateAgentTeamsRequest(request ExecutionRequest) error {
	if strings.TrimSpace(request.MissionID) == "" || strings.TrimSpace(request.StepID) == "" || strings.TrimSpace(request.LogicalActorID) == "" || strings.TrimSpace(request.RuntimePrincipalID) == "" || request.RuntimeBindingRevision < 1 || strings.TrimSpace(string(request.AgentFunction)) == "" || strings.TrimSpace(request.EnvironmentID) == "" || strings.TrimSpace(request.AgentTeamsInstanceID) == "" {
		return fmt.Errorf("%w: AgentTeams mission, binding, function, environment, and instance are required", ErrInvalidExecutionRequest)
	}
	return nil
}

type agentTeamsHandle struct {
	session AgentTeamsSession
	request ExecutionRequest

	once        sync.Once
	cancelErr   error
	errorMu     sync.RWMutex
	terminalErr error
}

func (h *agentTeamsHandle) Events(ctx context.Context) <-chan ExecutionEvent {
	output := make(chan ExecutionEvent)
	input := h.session.Events(ctx, h.request.Cursor)
	go func() {
		defer close(output)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-input:
				if !ok {
					h.captureTerminalError(ctx)
					return
				}
				actor, diagnostic := mapAgentTeamsActor(event.ActorID, event.ActorRole)
				mapped := ExecutionEvent{
					RunID: event.RunID, StepID: event.StepID, Kind: event.Kind, Cursor: event.Cursor, SourceEventID: event.SourceEventID, AdapterCursor: event.AdapterCursor, Summary: event.Summary,
					Artifacts: event.Artifacts, WorkspaceDigest: event.WorkspaceDigest, ContextHash: h.request.ContextHash,
					Actor: actor, ActorDiagnostic: diagnostic,
				}
				select {
				case output <- mapped:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return output
}

func (h *agentTeamsHandle) captureTerminalError(ctx context.Context) {
	failures, ok := h.session.(interface {
		Errors(context.Context) <-chan error
	})
	if !ok {
		return
	}
	select {
	case err := <-failures.Errors(ctx):
		if err != nil {
			h.errorMu.Lock()
			h.terminalErr = err
			h.errorMu.Unlock()
		}
	default:
	}
}

func (h *agentTeamsHandle) TerminalError() error {
	h.errorMu.RLock()
	defer h.errorMu.RUnlock()
	return h.terminalErr
}

func (h *agentTeamsHandle) Cancel(ctx context.Context) error {
	h.once.Do(func() { h.cancelErr = h.session.Cancel(ctx) })
	return h.cancelErr
}

func mapAgentTeamsActor(id, role string) (model.Actor, string) {
	id = strings.TrimSpace(id)
	role = strings.ToLower(strings.TrimSpace(role))
	if id == "" && role == "" {
		return model.Actor{}, ""
	}
	if id == "" {
		return model.Actor{}, "AgentTeams actor id is required"
	}
	switch role {
	case "agent", "worker", string(model.FunctionManager), string(model.FunctionDeliveryLeader), string(model.FunctionResearch), string(model.FunctionBuild), string(model.FunctionVerify):
		return model.Actor{ID: id, Kind: model.ActorAgent, Role: model.RoleAgent}, ""
	case "human":
		return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleContributor}, ""
	case string(model.RoleOwner):
		return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleOwner}, ""
	case string(model.RoleLead):
		return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleLead}, ""
	case string(model.RoleContributor):
		return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleContributor}, ""
	case string(model.RoleReviewer):
		return model.Actor{ID: id, Kind: model.ActorHuman, Role: model.RoleReviewer}, ""
	default:
		return model.Actor{}, fmt.Sprintf("unknown AgentTeams role %q", role)
	}
}

var _ ExecutorAdapter = (*AgentTeamsAdapter)(nil)

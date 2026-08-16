// Package executor defines the boundary between Haowork Core and external run lifecycles.
package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

var ErrInvalidExecutionRequest = errors.New("invalid execution request")

type ExecutionRequest struct {
	RunID, TaskID, StepID               string
	MissionID, WorkItemID               string
	GoalVersion                         int
	ContextID, ContextHash              string
	Actor                               model.Actor
	LogicalActorID                      string
	RuntimePrincipalID                  string
	RuntimeBindingRevision              int
	AgentFunction                       model.AgentFunction
	EnvironmentID, AgentTeamsInstanceID string
	Cursor                              string
}

type ExecutionEvent struct {
	RunID, StepID, Kind, Cursor, SourceEventID, AdapterCursor, Summary string
	Artifacts                                                          []model.ArtifactRef
	WorkspaceDigest                                                    string
	ContextHash                                                        string
	Actor                                                              model.Actor
	ActorDiagnostic                                                    string
}

type ExecutionHandle interface {
	Events(context.Context) <-chan ExecutionEvent
	Cancel(context.Context) error
}

type ExecutorAdapter interface {
	Start(context.Context, ExecutionRequest) (ExecutionHandle, error)
}

func validateRequest(request ExecutionRequest) error {
	if strings.TrimSpace(request.RunID) == "" {
		return fmt.Errorf("%w: run id is required", ErrInvalidExecutionRequest)
	}
	if strings.TrimSpace(request.TaskID) == "" {
		return fmt.Errorf("%w: task id is required", ErrInvalidExecutionRequest)
	}
	if request.GoalVersion < 1 {
		return fmt.Errorf("%w: goal version is required", ErrInvalidExecutionRequest)
	}
	if strings.TrimSpace(request.ContextID) == "" {
		return fmt.Errorf("%w: context id is required", ErrInvalidExecutionRequest)
	}
	if strings.TrimSpace(request.ContextHash) == "" {
		return fmt.Errorf("%w: context hash is required", ErrInvalidExecutionRequest)
	}
	if strings.TrimSpace(request.Actor.ID) == "" {
		return fmt.Errorf("%w: actor id is required", ErrInvalidExecutionRequest)
	}
	return nil
}

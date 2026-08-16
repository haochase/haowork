// Package evidence independently verifies task evidence before it can satisfy a completion gate.
package evidence

import (
	"context"
	"os/exec"

	"github.com/haochase/haowork/internal/model"
)

type EvidenceCandidate struct {
	TaskID, RunID, ContextID, Kind, URI, SHA256, Command, Outcome string
	Actor                                                         model.Actor
}

type CommandResult struct {
	ExitCode       int
	Stdout, Stderr string
}

type CommandRunner interface {
	Run(context.Context, []string, string) (CommandResult, error)
}

type StateProvider interface {
	Snapshot(context.Context) (model.ProjectState, error)
}

type EvidenceDecision struct {
	EvidenceID, Status string
	Checks             []model.EvidenceCheck
}

type EvidenceVerifier interface {
	Verify(context.Context, EvidenceCandidate) (EvidenceDecision, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, argv []string, dir string) (CommandResult, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = dir
	output, err := command.Output()
	result := CommandResult{Stdout: string(output)}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
		result.Stderr = string(exitErr.Stderr)
		return result, nil
	}
	return result, err
}

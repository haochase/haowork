package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/spf13/cobra"
)

type planOutput struct {
	Requirement model.Requirement `json:"requirement"`
	Tasks       []model.Task      `json:"tasks"`
}

type approvalOutput struct {
	RequirementID string `json:"requirement_id"`
}

type runFinishedOutput struct {
	RunID string `json:"run_id"`
}

type completionOutput struct {
	TaskID string `json:"task_id"`
}

type historyOutput struct {
	Events []model.Event `json:"events"`
}

func openProject(ctx context.Context, start string) (core.Project, error) {
	return openProjectWithDependencies(ctx, start, nil)
}

func openProjectWithDependencies(ctx context.Context, start string, deps *Dependencies) (core.Project, error) {
	var transfer *core.TransferConfig
	if deps != nil {
		transfer = deps.Transfer
	}
	if transfer == nil {
		root, err := capsule.Find(start)
		if err != nil {
			return core.Project{}, operationalError(err)
		}
		provider := TransferConfigProvider(LocalTransferConfigProvider{})
		if deps != nil && deps.TransferProvider != nil {
			provider = deps.TransferProvider
		}
		transfer, err = provider.Load(ctx, root)
		if err != nil {
			return core.Project{}, operationalError(fmt.Errorf("load transfer configuration: %w", err))
		}
	}
	project, err := core.Open(ctx, start, core.Dependencies{
		IDs: idgen.New(), Clock: systemClock{}, Transfer: transfer,
	})
	if err != nil {
		return core.Project{}, operationalError(err)
	}
	return project, nil
}

func localAPIClient(ctx context.Context, start string) *localapi.Client {
	root, err := capsule.Find(start)
	if err != nil {
		return nil
	}
	manifest, err := capsule.Load(root)
	if err != nil {
		return nil
	}
	metadata, err := localcore.ReadMetadata(root)
	if err != nil || metadata.ProjectID != manifest.ProjectID || !localcore.IsHealthy(ctx, metadata) {
		return nil
	}
	return localapi.NewClient(metadata)
}

func writeOutput(w io.Writer, jsonMode bool, human string, structured any) error {
	if jsonMode {
		return operationalError(json.NewEncoder(w).Encode(structured))
	}
	_, err := fmt.Fprintln(w, human)
	return operationalError(err)
}

func actorFromFlags(id, kind, role string) (model.Actor, error) {
	id = strings.TrimSpace(id)
	kind = strings.TrimSpace(kind)
	role = strings.TrimSpace(role)
	if id == "" {
		return model.Actor{}, errors.New("actor id is required")
	}
	if kind == "" {
		if role == string(model.RoleAgent) {
			kind = string(model.ActorAgent)
		} else {
			kind = string(model.ActorHuman)
		}
	}
	if role == "" {
		if kind == string(model.ActorAgent) {
			role = string(model.RoleAgent)
		} else {
			role = string(model.RoleOwner)
		}
	}
	return model.Actor{ID: id, Kind: model.ActorKind(kind), Role: model.ActorRole(role)}, nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", operationalError(err)
	}
	defer file.Close()

	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", operationalError(err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func operationalError(err error) error {
	if err == nil {
		return nil
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return err
	}
	return &CodedError{Code: ExitFailure, Err: err}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	var coded *CodedError
	if errors.As(err, &coded) {
		return err
	}
	if strings.Contains(err.Error(), "Team credential unavailable") {
		return &CodedError{Code: ExitOffline, Err: err}
	}
	var teamAPIError *teamapi.APIError
	if errors.As(err, &teamAPIError) {
		switch teamAPIError.StatusCode {
		case 401, 503:
			return &CodedError{Code: ExitOffline, Err: err}
		case 403:
			return &CodedError{Code: ExitApproval, Err: err}
		case 409:
			return &CodedError{Code: ExitConflict, Err: err}
		case 422:
			return &CodedError{Code: ExitGate, Err: err}
		}
	}
	var teamConflict *teamapi.ConflictError
	if errors.As(err, &teamConflict) {
		return &CodedError{Code: ExitConflict, Err: err}
	}
	if errors.Is(err, teamsync.ErrOffline) || errors.Is(err, team.ErrNotWritable) {
		return &CodedError{Code: ExitOffline, Err: err}
	}
	if errors.Is(err, teamsync.ErrStaleCursor) {
		return &CodedError{Code: ExitConflict, Err: err}
	}
	var apiError *localapi.HTTPError
	if errors.As(err, &apiError) {
		switch apiError.StatusCode {
		case 400:
			return errors.New(apiError.Error())
		case 401, 503:
			return &CodedError{Code: ExitOffline, Err: apiError}
		case 403:
			return &CodedError{Code: ExitApproval, Err: apiError}
		case 409:
			return &CodedError{Code: ExitConflict, Err: apiError}
		case 422:
			return &CodedError{Code: ExitGate, Err: apiError}
		default:
			return operationalError(apiError)
		}
	}
	switch {
	case errors.Is(err, app.ErrConflict):
		return &CodedError{Code: ExitConflict, Err: err}
	case errors.Is(err, app.ErrGateFailed):
		return &CodedError{Code: ExitGate, Err: err}
	case errors.Is(err, app.ErrApprovalRequired):
		return &CodedError{Code: ExitApproval, Err: err}
	case errors.Is(err, app.ErrOperational):
		if cause := applicationOperationalCause(err); cause != nil && isOperationalError(cause) {
			return operationalError(cause)
		}
		return operationalError(err)
	case isOperationalError(err):
		return operationalError(err)
	default:
		return err
	}
}

func applicationOperationalCause(err error) error {
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return nil
	}
	for _, cause := range multi.Unwrap() {
		if !errors.Is(cause, app.ErrOperational) {
			return cause
		}
	}
	return nil
}

func isOperationalError(err error) bool {
	var pathError *os.PathError
	var linkError *os.LinkError
	var syscallError *os.SyscallError
	var errno syscall.Errno
	return errors.As(err, &pathError) ||
		errors.As(err, &linkError) ||
		errors.As(err, &syscallError) ||
		errors.As(err, &errno) ||
		errors.Is(err, eventstore.ErrHistoryCorrupt) ||
		errors.Is(err, eventstore.ErrStoreBusy) ||
		errors.Is(err, eventstore.ErrEventTooLarge) ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, os.ErrPermission) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func NewPlanCommand(deps *Dependencies) *cobra.Command {
	planCommand := &cobra.Command{
		Use:   "plan",
		Short: "Manage governed requirements and tasks",
		Args:  cobra.NoArgs,
	}

	var title string
	var taskTitles []string
	var acceptanceCriteria []string
	var constraints []string
	var actorID string
	var role string
	createCommand := &cobra.Command{
		Use:   "create",
		Short: "Create a draft requirement and its tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(taskTitles) != len(acceptanceCriteria) {
				return &CodedError{Code: ExitUsage, Err: errors.New("--task and --acceptance counts must match")}
			}
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			tasks := make([]app.TaskInput, len(taskTitles))
			for index := range taskTitles {
				tasks[index] = app.TaskInput{
					Title:              taskTitles[index],
					AcceptanceCriteria: []string{acceptanceCriteria[index]},
				}
			}
			input := app.PlanInput{
				Title:       title,
				Constraints: constraints,
				Tasks:       tasks,
				Actor:       actor,
			}
			var requirement model.Requirement
			var plannedTasks []model.Task
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				response, err := client.Plan(cmd.Context(), input)
				if err != nil {
					return mapError(err)
				}
				requirement, plannedTasks = response.Requirement, response.Tasks
			} else {
				project, err := openProject(cmd.Context(), deps.Options.Project)
				if err != nil {
					return mapError(err)
				}
				requirement, plannedTasks, err = project.Service.Plan(cmd.Context(), input)
				if err != nil {
					return mapError(err)
				}
			}
			return writeOutput(
				cmd.OutOrStdout(),
				deps.Options.JSON,
				fmt.Sprintf("created requirement %s with %d task(s)", requirement.ID, len(plannedTasks)),
				planOutput{Requirement: requirement, Tasks: plannedTasks},
			)
		},
	}
	createCommand.Flags().StringVar(&title, "title", "", "requirement title")
	createCommand.Flags().StringArrayVar(&taskTitles, "task", nil, "task title")
	createCommand.Flags().StringArrayVar(&acceptanceCriteria, "acceptance", nil, "task acceptance criterion")
	createCommand.Flags().StringArrayVar(&constraints, "constraint", nil, "requirement constraint")
	createCommand.Flags().StringVar(&actorID, "actor", "", "actor attribution")
	createCommand.Flags().StringVar(&role, "role", "", "actor role")
	_ = createCommand.MarkFlagRequired("title")
	_ = createCommand.MarkFlagRequired("task")
	_ = createCommand.MarkFlagRequired("acceptance")
	_ = createCommand.MarkFlagRequired("actor")
	_ = createCommand.MarkFlagRequired("role")
	planCommand.AddCommand(createCommand)
	return planCommand
}

func NewApproveCommand(deps *Dependencies) *cobra.Command {
	var actorID string
	var role string
	cmd := &cobra.Command{
		Use:   "approve REQUIREMENT-ID",
		Short: "Approve a draft requirement",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				if err := client.Approve(cmd.Context(), args[0], actor); err != nil {
					return mapError(err)
				}
			} else {
				project, err := openProject(cmd.Context(), deps.Options.Project)
				if err != nil {
					return mapError(err)
				}
				if err := project.Service.Approve(cmd.Context(), args[0], actor); err != nil {
					return mapError(err)
				}
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("approved requirement %s", args[0]),
				approvalOutput{RequirementID: args[0]},
			)
		},
	}
	cmd.Flags().StringVar(&actorID, "actor", "", "actor attribution")
	cmd.Flags().StringVar(&role, "role", "", "actor role")
	_ = cmd.MarkFlagRequired("actor")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func NewRunCommand(deps *Dependencies) *cobra.Command {
	runCommand := &cobra.Command{
		Use:   "run",
		Short: "Manage governed task runs",
		Args:  cobra.NoArgs,
	}

	var executor string
	var contextID string
	var startActorID string
	var startActorKind string
	startCommand := &cobra.Command{
		Use:   "start TASK-ID",
		Short: "Start an approved task run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(startActorID, startActorKind, "")
			if err != nil {
				return err
			}
			var run model.Run
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				run, err = client.StartRunWithContext(cmd.Context(), args[0], executor, contextID, actor)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				if contextID == "" {
					run, err = project.Service.StartRun(cmd.Context(), args[0], executor, actor)
				} else {
					run, err = project.Service.StartRunWithContext(cmd.Context(), args[0], executor, contextID, actor)
				}
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("started run %s", run.ID), run)
		},
	}
	startCommand.Flags().StringVar(&executor, "executor", "", "run executor")
	startCommand.Flags().StringVar(&contextID, "context-id", "", "current context slice for this run")
	startCommand.Flags().StringVar(&startActorID, "actor", "", "actor attribution")
	startCommand.Flags().StringVar(&startActorKind, "actor-kind", "", "actor kind")
	_ = startCommand.MarkFlagRequired("executor")
	_ = startCommand.MarkFlagRequired("actor")
	_ = startCommand.MarkFlagRequired("actor-kind")

	var result string
	var finishActorID string
	var finishActorKind string
	finishCommand := &cobra.Command{
		Use:   "finish RUN-ID",
		Short: "Finish a running task run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(finishActorID, finishActorKind, "")
			if err != nil {
				return err
			}
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				if err := client.FinishRun(cmd.Context(), args[0], result, actor); err != nil {
					return mapError(err)
				}
			} else {
				project, err := openProject(cmd.Context(), deps.Options.Project)
				if err != nil {
					return mapError(err)
				}
				if err := project.Service.FinishRun(cmd.Context(), args[0], result, actor); err != nil {
					return mapError(err)
				}
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("finished run %s", args[0]),
				runFinishedOutput{RunID: args[0]},
			)
		},
	}
	finishCommand.Flags().StringVar(&result, "result", "", "run result")
	finishCommand.Flags().StringVar(&finishActorID, "actor", "", "actor attribution")
	finishCommand.Flags().StringVar(&finishActorKind, "actor-kind", "", "actor kind")
	_ = finishCommand.MarkFlagRequired("result")
	_ = finishCommand.MarkFlagRequired("actor")
	_ = finishCommand.MarkFlagRequired("actor-kind")

	runCommand.AddCommand(startCommand, finishCommand)
	return runCommand
}

func NewVerifyCommand(deps *Dependencies) *cobra.Command {
	var kind string
	var evidencePath string
	var outcome string
	var runID string
	var contextID string
	var command string
	var suppliedSHA256 string
	var actorID string
	var role string
	cmd := &cobra.Command{
		Use:   "verify TASK-ID",
		Short: "Record evidence for a finished task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			digest, err := digestFile(evidencePath)
			if err != nil {
				return err
			}
			if suppliedSHA256 != "" {
				digest = suppliedSHA256
			}
			if runID != "" || contextID != "" || command != "" {
				if runID == "" || contextID == "" || command == "" {
					return errors.New("run-id, context-id, and command are required for independent evidence verification")
				}
				candidateInput := evidence.EvidenceCandidate{
					TaskID: args[0], RunID: runID, ContextID: contextID, Kind: kind, URI: evidencePath, SHA256: digest,
					Command: command, Outcome: outcome, Actor: actor,
				}
				var candidate model.Evidence
				if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
					candidate, err = client.RecordEvidenceCandidate(cmd.Context(), candidateInput)
					if err == nil {
						candidate, err = client.VerifyEvidence(cmd.Context(), candidate.ID, actor)
					}
				} else {
					project, openErr := openProject(cmd.Context(), deps.Options.Project)
					if openErr != nil {
						return mapError(openErr)
					}
					candidate, err = project.Service.RecordEvidenceCandidate(cmd.Context(), candidateInput)
					if err == nil {
						candidate, err = project.Service.VerifyEvidence(cmd.Context(), candidate.ID, actor)
					}
				}
				if err != nil {
					return mapError(err)
				}
				return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("verified evidence %s", candidate.ID), candidate)
			}
			input := app.VerifyInput{
				TaskID:  args[0],
				Kind:    kind,
				URI:     evidencePath,
				SHA256:  digest,
				Outcome: outcome,
				Actor:   actor,
			}
			var evidence model.Evidence
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				evidence, err = client.Verify(cmd.Context(), input)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				evidence, err = project.Service.Verify(cmd.Context(), input)
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("recorded evidence %s", evidence.ID), evidence)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "evidence kind")
	cmd.Flags().StringVar(&evidencePath, "evidence", "", "evidence file")
	cmd.Flags().StringVar(&outcome, "outcome", "", "evidence outcome")
	cmd.Flags().StringVar(&runID, "run-id", "", "run bound to the evidence candidate")
	cmd.Flags().StringVar(&contextID, "context-id", "", "context bound to the evidence candidate")
	cmd.Flags().StringVar(&command, "command", "", "argv command independently executed by the verifier")
	cmd.Flags().StringVar(&suppliedSHA256, "sha256", "", "expected SHA-256 of the evidence file")
	cmd.Flags().StringVar(&actorID, "actor", "", "actor attribution")
	cmd.Flags().StringVar(&role, "role", "", "actor role")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("evidence")
	_ = cmd.MarkFlagRequired("outcome")
	_ = cmd.MarkFlagRequired("actor")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func NewCompleteCommand(deps *Dependencies) *cobra.Command {
	var actorID string
	var role string
	cmd := &cobra.Command{
		Use:   "complete TASK-ID",
		Short: "Complete a verified task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				if err := client.Complete(cmd.Context(), args[0], actor); err != nil {
					return mapError(err)
				}
			} else {
				project, err := openProject(cmd.Context(), deps.Options.Project)
				if err != nil {
					return mapError(err)
				}
				if err := project.Service.Complete(cmd.Context(), args[0], actor); err != nil {
					return mapError(err)
				}
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("completed task %s", args[0]),
				completionOutput{TaskID: args[0]},
			)
		},
	}
	cmd.Flags().StringVar(&actorID, "actor", "", "actor attribution")
	cmd.Flags().StringVar(&role, "role", "", "actor role")
	_ = cmd.MarkFlagRequired("actor")
	_ = cmd.MarkFlagRequired("role")
	return cmd
}

func NewHistoryCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "history [AGGREGATE-ID]",
		Short: "Show project or aggregate event history",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			aggregateID := ""
			if len(args) == 1 {
				aggregateID = args[0]
			}
			var events []model.Event
			var err error
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				events, err = client.History(cmd.Context(), aggregateID)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				events, err = project.Service.History(cmd.Context(), aggregateID)
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("%d event(s)", len(events)),
				historyOutput{Events: events},
			)
		},
	}
}

package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/spf13/cobra"
)

func DefaultCommands() []CommandFactory {
	return []CommandFactory{
		NewInitCommand,
		NewServeCommand,
		NewOpenCommand,
		NewStopCommand,
		NewTeamCommand,
		NewPlanCommand,
		NewApproveCommand,
		NewRunCommand,
		NewVerifyCommand,
		NewCompleteCommand,
		NewHistoryCommand,
		NewStatusCommand,
		NewRecordCommand,
		NewContextCommand,
		NewAgentTeamsCommand,
		NewAgentsCommand,
		NewSkillsCommand,
		NewTraceCommand,
		NewApprovalsCommand,
		NewTransferCommand,
	}
}

type localCoreOutput struct {
	Endpoint string `json:"endpoint"`
}

func NewServeCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "serve [PROJECT]",
		Short: "Run the local Core for a governed project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := commandProject(deps.Options.Project, args)
			project, err := openProjectWithDependencies(cmd.Context(), start, deps)
			if err != nil {
				return mapError(err)
			}
			manager := localcore.NewManager(nil)
			server := &localapi.Server{Project: project, Team: project.Team, Changes: changes.Scanner{}}
			err = server.Serve(func(handler http.Handler) error {
				return manager.ServeWithHandler(cmd.Context(), start, func(metadata localcore.Metadata, stop func()) http.Handler {
					server.Metadata = metadata
					server.Stop = stop
					return handler
				})
			})
			if err != nil {
				return operationalError(err)
			}
			return nil
		},
	}
}

type recordScanOutput struct {
	Changes []model.FileChange `json:"changes"`
}

type recordAttributeOutput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	TaskID string `json:"task_id"`
	Note   string `json:"note,omitempty"`
}

func NewRecordCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "record",
		Short: "Record current workspace changes and their attribution",
		Args:  cobra.NoArgs,
	}

	var scanActorID string
	var scanRole string
	scanCommand := &cobra.Command{
		Use:   "scan",
		Short: "Scan and record current Git workspace changes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(scanActorID, "", scanRole)
			if err != nil {
				return err
			}
			var fileChanges []model.FileChange
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				fileChanges, err = client.ScanChanges(cmd.Context(), actor)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				fileChanges, err = (changes.Scanner{}).Scan(cmd.Context(), project.Root)
				if err == nil {
					err = project.Service.RecordScan(cmd.Context(), fileChanges, actor)
				}
			}
			if err != nil {
				return mapError(operationalError(err))
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("recorded %d workspace change(s)", len(fileChanges)), recordScanOutput{Changes: fileChanges})
		},
	}
	scanCommand.Flags().StringVar(&scanActorID, "actor", "", "actor attribution")
	scanCommand.Flags().StringVar(&scanRole, "role", "", "actor role")
	_ = scanCommand.MarkFlagRequired("actor")
	_ = scanCommand.MarkFlagRequired("role")

	var sha256 string
	var taskID string
	var note string
	var attributeActorID string
	var attributeRole string
	attributeCommand := &cobra.Command{
		Use:   "attribute PATH",
		Short: "Attribute an exact scanned file version to a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(attributeActorID, "", attributeRole)
			if err != nil {
				return err
			}
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				err = client.AttributeChange(cmd.Context(), args[0], sha256, taskID, note, actor)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				err = project.Service.AttributeChange(cmd.Context(), args[0], sha256, taskID, note, actor)
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("attributed %s", args[0]), recordAttributeOutput{Path: args[0], SHA256: sha256, TaskID: taskID, Note: note})
		},
	}
	attributeCommand.Flags().StringVar(&sha256, "sha256", "", "current file SHA-256")
	attributeCommand.Flags().StringVar(&taskID, "task", "", "known task ID or external-manual")
	attributeCommand.Flags().StringVar(&note, "note", "", "manual attribution note")
	attributeCommand.Flags().StringVar(&attributeActorID, "actor", "", "actor attribution")
	attributeCommand.Flags().StringVar(&attributeRole, "role", "", "actor role")
	_ = attributeCommand.MarkFlagRequired("task")
	_ = attributeCommand.MarkFlagRequired("actor")
	_ = attributeCommand.MarkFlagRequired("role")

	command.AddCommand(scanCommand, attributeCommand)
	return command
}

func NewOpenCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "open [PROJECT]",
		Short: "Open the local Workbench for a governed project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := commandProject(deps.Options.Project, args)
			project, err := openProjectWithDependencies(cmd.Context(), start, deps)
			if err != nil {
				return mapError(err)
			}
			root, err := filepath.Abs(project.Root)
			if err != nil {
				return operationalError(err)
			}
			metadata, err := startOrReuseLocalCore(cmd.Context(), root, project.Manifest.ProjectID)
			if err != nil {
				return operationalError(err)
			}
			token, err := localapi.NewClient(metadata).CreateBrowserSession(cmd.Context())
			if err != nil {
				return mapError(err)
			}
			if err := localcore.OpenBrowser(cmd.Context(), localcore.SystemBrowser{}, metadata.Endpoint, token); err != nil {
				return operationalError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "opened local workbench", localCoreOutput{Endpoint: metadata.Endpoint})
		},
	}
}

func NewStopCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "stop [PROJECT]",
		Short: "Stop the local Core for a governed project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := commandProject(deps.Options.Project, args)
			if err := localcore.NewManager(nil).StopVerified(cmd.Context(), start); err != nil {
				return operationalError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "stopped local Core", struct{}{})
		},
	}
}

func commandProject(option string, args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return option
}

func startOrReuseLocalCore(ctx context.Context, root, projectID string) (localcore.Metadata, error) {
	return startOrReuseLocalCoreWithSpawner(ctx, root, projectID, spawnLocalCore)
}

func startOrReuseLocalCoreWithSpawner(ctx context.Context, root, projectID string, spawn func(string) error) (localcore.Metadata, error) {
	var result localcore.Metadata
	err := localcore.WithLaunchLock(ctx, root, func() error {
		if metadata, err := localcore.ReadMetadata(root); err == nil && metadata.ProjectID == projectID && localcore.IsHealthy(ctx, metadata) {
			result = metadata
			return nil
		}
		if err := spawn(root); err != nil {
			return err
		}

		waitContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			metadata, err := localcore.ReadMetadata(root)
			if err == nil && metadata.ProjectID == projectID && localcore.IsHealthy(waitContext, metadata) {
				result = metadata
				return nil
			}
			select {
			case <-waitContext.Done():
				return fmt.Errorf("local Core did not become healthy: %w", waitContext.Err())
			case <-ticker.C:
			}
		}
	})
	if err != nil {
		return localcore.Metadata{}, err
	}
	return result, nil
}

func spawnLocalCore(root string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate haowork executable: %w", err)
	}
	child := exec.Command(executable, "serve", "--project", root)
	if err := child.Start(); err != nil {
		return fmt.Errorf("start local Core: %w", err)
	}
	go func() { _ = child.Wait() }()
	return nil
}

func NewInitCommand(deps *Dependencies) *cobra.Command {
	var name string
	var actor string
	var goal string
	var doneWhen []string
	var invariants []string
	var projectID string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a portable Haowork project capsule",
		Long:  "Initialize a portable Haowork project capsule and durable GoalVersion v1. The supplied actor is attributed as the local human/owner; this is attribution only and does not authenticate the actor.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest, err := app.InitializeProject(cmd.Context(), app.InitializeProjectInput{
				Root:               deps.Options.Project,
				Name:               name,
				ProjectID:          projectID,
				Goal:               goal,
				Invariants:         invariants,
				CompletionCriteria: doneWhen,
				Actor: model.Actor{
					ID:   actor,
					Kind: model.ActorHuman,
					Role: model.RoleOwner,
				},
			}, idgen.New(), systemClock{})
			if err != nil {
				return mapError(err)
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("initialized project %s (%s)", manifest.Name, manifest.ProjectID),
				manifest,
			)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project name")
	cmd.Flags().StringVar(&actor, "actor", "", "local human/owner attribution")
	cmd.Flags().StringVar(&goal, "goal", "", "GoalVersion v1 statement")
	cmd.Flags().StringArrayVar(&doneWhen, "done-when", nil, "GoalVersion v1 completion criterion")
	cmd.Flags().StringArrayVar(&invariants, "invariant", nil, "GoalVersion v1 invariant")
	cmd.Flags().StringVar(&projectID, "project-id", "", "project ID")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("actor")
	_ = cmd.MarkFlagRequired("goal")
	_ = cmd.MarkFlagRequired("done-when")
	return cmd
}

func NewStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the current governed project state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var state model.ProjectState
			var err error
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				state, err = client.Status(cmd.Context())
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				state, err = project.Service.Status(cmd.Context())
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(
				cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf(
					"project %s goal v%d: %d requirement(s), %d task(s), %d run(s)",
					state.ProjectID, state.Goal.Version, len(state.Requirements), len(state.Tasks), len(state.Runs),
				),
				state,
			)
		},
	}
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

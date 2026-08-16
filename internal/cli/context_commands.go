package cli

import (
	"fmt"

	"github.com/haochase/haowork/internal/app"
	"github.com/spf13/cobra"
)

func NewContextCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "context",
		Short: "Build and inspect task context slices",
		Args:  cobra.NoArgs,
	}

	var sources []string
	var allowedPaths []string
	var deniedPaths []string
	var reason string
	var actorID string
	var actorKind string
	var actorRole string
	var supersedesID string
	build := &cobra.Command{
		Use:   "build TASK-ID",
		Short: "Build an immutable context slice for an approved task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := actorFromFlags(actorID, actorKind, actorRole)
			if err != nil {
				return err
			}
			input := app.ContextBuildInput{
				TaskID: args[0], SupersedesID: supersedesID, Reason: reason, Sources: sources,
				AllowedPaths: allowedPaths, DeniedPaths: deniedPaths, Actor: actor,
			}
			var sliceOutput any
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				sliceOutput, err = client.BuildContext(cmd.Context(), input)
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				sliceOutput, err = project.Service.BuildContext(cmd.Context(), input)
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("built context for %s", args[0]), sliceOutput)
		},
	}
	build.Flags().StringArrayVar(&sources, "source", nil, "project-relative source reference")
	build.Flags().StringArrayVar(&allowedPaths, "allow", nil, "allowed project-relative path")
	build.Flags().StringArrayVar(&deniedPaths, "deny", nil, "denied project-relative path")
	build.Flags().StringVar(&reason, "reason", "", "context purpose")
	build.Flags().StringVar(&supersedesID, "supersedes", "", "prior context slice to supersede")
	build.Flags().StringVar(&actorID, "actor", "", "actor attribution")
	build.Flags().StringVar(&actorKind, "actor-kind", "", "actor kind")
	build.Flags().StringVar(&actorRole, "role", "", "actor role")
	_ = build.MarkFlagRequired("source")
	_ = build.MarkFlagRequired("actor")
	_ = build.MarkFlagRequired("role")

	show := &cobra.Command{
		Use:   "show CONTEXT-ID",
		Short: "Show one immutable context slice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var (
				sliceOutput any
				err         error
			)
			if client := localAPIClient(cmd.Context(), deps.Options.Project); client != nil {
				sliceOutput, err = client.GetContext(cmd.Context(), args[0])
			} else {
				project, openErr := openProject(cmd.Context(), deps.Options.Project)
				if openErr != nil {
					return mapError(openErr)
				}
				sliceOutput, err = project.Service.GetContext(cmd.Context(), args[0])
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("context %s", args[0]), sliceOutput)
		},
	}
	command.AddCommand(build, show)
	return command
}

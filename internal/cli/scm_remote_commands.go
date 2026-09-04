package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSCMGitHubCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "github", Short: "Observe GitHub pull requests and checks", Args: cobra.NoArgs}
	command.AddCommand(newSCMGitHubConnectCommand(deps), newSCMGitHubSyncCommand(deps), newSCMGitHubStatusCommand(deps))
	return command
}

func newSCMGitHubConnectCommand(deps *Dependencies) *cobra.Command {
	var actorID, role string
	command := &cobra.Command{
		Use: "connect", Short: "Connect the registered Git repository to GitHub read-only observation", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(command, deps)
			if err != nil {
				return err
			}
			remote, err := client.ConnectGitHubSCM(command.Context(), actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(command.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("connected GitHub remote %s", remote.ID), remote)
		},
	}
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	_ = command.MarkFlagRequired("actor")
	_ = command.MarkFlagRequired("role")
	return command
}

func newSCMGitHubSyncCommand(deps *Dependencies) *cobra.Command {
	var actorID, role string
	command := &cobra.Command{
		Use: "sync", Short: "Synchronize GitHub observations without remote writes", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(command, deps)
			if err != nil {
				return err
			}
			report, err := client.SyncGitHubSCM(command.Context(), actor)
			if err != nil {
				return mapError(err)
			}
			human := fmt.Sprintf("GitHub sync observed %d ref(s), %d pull request(s), %d review(s), and %d check(s); appended %d fact(s)", report.Refs, report.PullRequests, report.Reviews, report.Checks, report.Appended)
			return writeOutput(command.OutOrStdout(), deps.Options.JSON, human, report)
		},
	}
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	_ = command.MarkFlagRequired("actor")
	_ = command.MarkFlagRequired("role")
	return command
}

func newSCMGitHubStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show GitHub observations and deterministic local links", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			client, err := scmLocalClient(command, deps)
			if err != nil {
				return err
			}
			status, err := client.GitHubSCMStatus(command.Context())
			if err != nil {
				return mapError(err)
			}
			human := fmt.Sprintf("GitHub observations: %d ref(s), %d pull request(s), %d review(s), %d check(s)", len(status.Refs), len(status.PullRequests), len(status.Reviews), len(status.Checks))
			return writeOutput(command.OutOrStdout(), deps.Options.JSON, human, status)
		},
	}
}

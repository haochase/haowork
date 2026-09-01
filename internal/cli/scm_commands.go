package cli

import (
	"errors"
	"fmt"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/spf13/cobra"
)

func NewSCMCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "scm", Short: "Manage governed Git commit provenance", Args: cobra.NoArgs}
	command.AddCommand(
		newSCMRegisterCommand(deps),
		newSCMStatusCommand(deps),
		newSCMScanCommand(deps),
		newSCMShowCommand(deps),
		newSCMProposeCommand(deps),
		newSCMConfirmCommand(deps),
		newSCMRejectCommand(deps),
		newSCMVerifyHistoryCommand(deps),
		newSCMGitHubCommand(deps),
	)
	return command
}

func newSCMRegisterCommand(deps *Dependencies) *cobra.Command {
	var actorID, role string
	command := &cobra.Command{
		Use: "register", Short: "Register the project Git repository", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			repository, err := client.RegisterSCM(cmd.Context(), actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("registered SCM repository %s", repository.ID), repository)
		},
	}
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	_ = command.MarkFlagRequired("actor")
	_ = command.MarkFlagRequired("role")
	return command
}

func newSCMStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show repositories, commits, and bindings", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			status, err := client.SCMStatus(cmd.Context())
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("SCM: %d repository(s), %d commit(s), %d binding(s)", len(status.Repositories), len(status.Commits), len(status.Bindings)), status)
		},
	}
}

func newSCMScanCommand(deps *Dependencies) *cobra.Command {
	var repositoryID, commitOID, actorID, actorKind, role string
	command := &cobra.Command{
		Use: "scan", Short: "Observe one exact Git commit", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, actorKind, role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			observation, err := client.ObserveSCMCommit(cmd.Context(), repositoryID, commitOID, actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("observed commit %s", observation.CommitOID), observation)
		},
	}
	command.Flags().StringVar(&repositoryID, "repository", "", "registered SCM repository ID")
	command.Flags().StringVar(&commitOID, "commit", "", "complete Git commit object ID")
	command.Flags().StringVar(&actorID, "actor", "", "actor ID")
	command.Flags().StringVar(&actorKind, "actor-kind", "", "actor kind")
	command.Flags().StringVar(&role, "role", "", "actor role")
	for _, name := range []string{"repository", "commit", "actor"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newSCMShowCommand(deps *Dependencies) *cobra.Command {
	var commitOID string
	command := &cobra.Command{
		Use: "show", Short: "Show one observed commit", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			commit, err := client.SCMCommit(cmd.Context(), commitOID)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("commit %s is %s", commit.Observation.CommitOID, commit.Status), commit)
		},
	}
	command.Flags().StringVar(&commitOID, "commit", "", "observed Git commit object ID")
	_ = command.MarkFlagRequired("commit")
	return command
}

func newSCMProposeCommand(deps *Dependencies) *cobra.Command {
	var repositoryID, commitOID, missionID, actorID, actorKind, role string
	var taskIDs, evidenceIDs, traceIDs []string
	command := &cobra.Command{
		Use: "propose", Short: "Propose a commit governance binding", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, actorKind, role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			binding, err := client.ProposeSCMBinding(cmd.Context(), app.ProposeSCMBindingInput{
				RepositoryID: repositoryID, CommitOID: commitOID, TaskIDs: taskIDs, MissionID: missionID,
				EvidenceIDs: evidenceIDs, TraceIDs: traceIDs, Actor: actor,
			})
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("proposed SCM binding %s", binding.ID), binding)
		},
	}
	command.Flags().StringVar(&repositoryID, "repository", "", "registered SCM repository ID")
	command.Flags().StringVar(&commitOID, "commit", "", "observed Git commit object ID")
	command.Flags().StringArrayVar(&taskIDs, "task", nil, "governed task ID")
	command.Flags().StringVar(&missionID, "mission", "", "governed mission ID")
	command.Flags().StringArrayVar(&evidenceIDs, "evidence", nil, "projected evidence ID")
	command.Flags().StringArrayVar(&traceIDs, "trace", nil, "execution trace ID")
	command.Flags().StringVar(&actorID, "actor", "", "actor ID")
	command.Flags().StringVar(&actorKind, "actor-kind", "", "actor kind")
	command.Flags().StringVar(&role, "role", "", "actor role")
	for _, name := range []string{"repository", "commit", "task", "mission", "evidence", "actor"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newSCMConfirmCommand(deps *Dependencies) *cobra.Command {
	var bindingID, actorID, role string
	command := &cobra.Command{
		Use: "confirm", Short: "Confirm a proposed commit binding", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			binding, err := client.ConfirmSCMBinding(cmd.Context(), bindingID, actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("confirmed SCM binding %s", binding.ID), binding)
		},
	}
	command.Flags().StringVar(&bindingID, "binding", "", "SCM binding ID")
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	for _, name := range []string{"binding", "actor", "role"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newSCMRejectCommand(deps *Dependencies) *cobra.Command {
	var bindingID, reason, actorID, role string
	command := &cobra.Command{
		Use: "reject", Short: "Reject a proposed commit binding", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			binding, err := client.RejectSCMBinding(cmd.Context(), bindingID, reason, actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("rejected SCM binding %s", binding.ID), binding)
		},
	}
	command.Flags().StringVar(&bindingID, "binding", "", "SCM binding ID")
	command.Flags().StringVar(&reason, "reason", "", "rejection reason")
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	for _, name := range []string{"binding", "reason", "actor", "role"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newSCMVerifyHistoryCommand(deps *Dependencies) *cobra.Command {
	var repositoryID, actorID, role string
	var refs []string
	command := &cobra.Command{
		Use: "verify-history", Short: "Verify observed commits against accepted refs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := actorFromFlags(actorID, "", role)
			if err != nil {
				return err
			}
			client, err := scmLocalClient(cmd, deps)
			if err != nil {
				return err
			}
			report, err := client.VerifySCMHistory(cmd.Context(), repositoryID, refs, actor)
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON,
				fmt.Sprintf("checked %d commit(s), superseded %d binding source(s)", report.Checked, report.Superseded), report)
		},
	}
	command.Flags().StringVar(&repositoryID, "repository", "", "registered SCM repository ID")
	command.Flags().StringArrayVar(&refs, "ref", nil, "accepted fully qualified Git ref")
	command.Flags().StringVar(&actorID, "actor", "", "human actor ID")
	command.Flags().StringVar(&role, "role", "", "human actor role")
	for _, name := range []string{"repository", "ref", "actor", "role"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func scmLocalClient(cmd *cobra.Command, deps *Dependencies) (*localapi.Client, error) {
	client := localAPIClient(cmd.Context(), deps.Options.Project)
	if client == nil {
		return nil, operationalError(errors.New("SCM commands require a running local Core"))
	}
	return client, nil
}

package cli

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/teamai"
	"github.com/spf13/cobra"
)

func NewTeamAICommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "teamai", Short: "Check and bind explicitly pinned TeamAI assets", Args: cobra.NoArgs}
	var doctorRoot string
	doctor := &cobra.Command{Use: "doctor", Short: "Read TeamAI initialization and local asset diagnostics", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := capsule.Find(deps.Options.Project)
		if err != nil {
			root, err = filepath.Abs(deps.Options.Project)
			if err != nil {
				return errors.New("project path is unavailable")
			}
		}
		result, doctorErr := app.DoctorTeamAI(cmd.Context(), root, doctorRoot, teamai.Detector{})
		// Diagnostic failures belong in the report, so JSON retains every check.
		if doctorErr != nil && len(result.Doctor.Errors) == 0 {
			return mapTeamAIError(doctorErr)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "TeamAI status: "+result.Status, result)
	}}
	doctor.Flags().StringVar(&doctorRoot, "asset-root", "", "installed or checked-out TeamAI team asset root (default: project root)")
	var repo, revision, actorID, role string
	bind := &cobra.Command{Use: "bind", Short: "Record a Human Owner or Lead TeamAI binding", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		role = strings.TrimSpace(role)
		if role != string(model.RoleOwner) && role != string(model.RoleLead) {
			return &CodedError{Code: ExitUsage, Err: errors.New("--role must be owner or lead")}
		}
		actor, err := actorFromFlags(actorID, "", role)
		if err != nil {
			return err
		}
		if actor.Kind != model.ActorHuman || (actor.Role != model.RoleOwner && actor.Role != model.RoleLead) {
			return mapError(app.ErrApprovalRequired)
		}
		project, err := openProjectWithDependencies(cmd.Context(), deps.Options.Project, deps)
		if err != nil {
			return mapTeamAIError(err)
		}
		binding, err := project.Service.BindTeamAI(cmd.Context(), project.Root, app.BindTeamAIInput{TeamRepo: repo, TeamRevision: revision, Actor: actor})
		if err != nil {
			return mapTeamAIError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "TeamAI assets bound", binding)
	}}
	bind.Flags().StringVar(&repo, "team-repo", "", "TeamAI repository matching project configuration")
	bind.Flags().StringVar(&revision, "revision", "", "declared pinned revision matching project configuration")
	bind.Flags().StringVar(&actorID, "actor", "", "human actor attribution")
	bind.Flags().StringVar(&role, "role", "", "owner or lead")
	for _, flag := range []string{"team-repo", "revision", "actor", "role"} {
		_ = bind.MarkFlagRequired(flag)
	}
	var verifyRoot string
	verify := &cobra.Command{Use: "verify", Short: "Verify local asset bytes and TeamAI initialization", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		project, err := openProjectWithDependencies(cmd.Context(), deps.Options.Project, deps)
		if err != nil {
			return mapTeamAIError(err)
		}
		result, err := project.Service.VerifyTeamAI(cmd.Context(), project.Root, verifyRoot, teamai.Detector{})
		if err != nil {
			return mapTeamAIError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "TeamAI local assets verified; declared revision is not Git HEAD verification", result)
	}}
	verify.Flags().StringVar(&verifyRoot, "asset-root", "", "installed or checked-out TeamAI team asset root (default: project root)")
	command.AddCommand(doctor, bind, verify)
	return command
}

func mapTeamAIError(err error) error {
	var issue teamai.Issue
	if errors.As(err, &issue) {
		return &CodedError{Code: ExitGate, Err: err}
	}
	if errors.Is(err, app.ErrConflict) || errors.Is(err, app.ErrApprovalRequired) {
		return mapError(err)
	}
	return &CodedError{Code: ExitFailure, Err: errors.New("TeamAI operation could not be completed")}
}

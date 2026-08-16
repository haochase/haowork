package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/spf13/cobra"
)

func NewTeamCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "team", Short: "Manage Team Core collaboration"}
	command.AddCommand(newTeamServeCommand(deps), newTeamJoinCommand(deps), newTeamStatusCommand(deps), newTeamSyncCommand(deps), newTeamLeaseCommand(deps), newTeamGoalCommand(deps), newTeamConflictCommand(deps))
	return command
}

func newTeamServeCommand(deps *Dependencies) *cobra.Command {
	var listen, authFile, certificateFile, keyFile string
	command := &cobra.Command{Use: "serve", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := capsule.Find(deps.Options.Project)
		if err != nil {
			return mapError(err)
		}
		manifest, err := capsule.Load(root)
		if err != nil {
			return mapError(err)
		}
		authenticator, err := teamapi.LoadStaticAuthenticator(authFile)
		if err != nil {
			return mapError(err)
		}
		service, err := team.New(cmd.Context(), root, team.Dependencies{IDs: idgen.New(), Clock: systemClock{}})
		if err != nil {
			return mapError(err)
		}
		if err := service.Recover(cmd.Context()); err != nil {
			return mapError(err)
		}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return mapError(err)
		}
		defer listener.Close()
		go func() { <-cmd.Context().Done(); _ = listener.Close() }()
		return mapError((&teamapi.Server{ProjectID: manifest.ProjectID, Service: service, Authenticator: authenticator}).Serve(listener, certificateFile, keyFile))
	}}
	command.Flags().StringVar(&listen, "listen", "127.0.0.1:8787", "Team Core listen address")
	command.Flags().StringVar(&authFile, "auth-file", "", "static Team authenticator file")
	command.Flags().StringVar(&certificateFile, "tls-cert", "", "TLS certificate file")
	command.Flags().StringVar(&keyFile, "tls-key", "", "TLS key file")
	_ = command.MarkFlagRequired("auth-file")
	return command
}

func teamProject(ctx context.Context, deps *Dependencies) (core.Project, error) {
	return core.Open(ctx, deps.Options.Project, core.Dependencies{IDs: idgen.New(), Clock: systemClock{}, TeamTokens: deps.TeamTokens})
}

func teamRemote(ctx context.Context, deps *Dependencies) (core.Project, *teamapi.Client, error) {
	project, err := teamProject(ctx, deps)
	if err != nil {
		return core.Project{}, nil, err
	}
	if project.Team == nil {
		return core.Project{}, nil, errors.New("project is not joined to a Team Core")
	}
	return project, project.Team.Remote, nil
}

func newTeamJoinCommand(deps *Dependencies) *cobra.Command {
	var endpoint, deviceID, environmentID string
	command := &cobra.Command{Use: "join", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("team endpoint must be an absolute URL")
		}
		client, err := teamapi.NewClient(endpoint, teamTokenSource(deps.TeamTokens), nil)
		if err != nil {
			return err
		}
		root, err := capsule.Find(deps.Options.Project)
		if err != nil {
			return err
		}
		manifest, err := capsule.Load(root)
		if err != nil {
			return err
		}
		status, err := client.Status(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		if status.ProjectID != manifest.ProjectID {
			return fmt.Errorf("team project %q does not match local project %q", status.ProjectID, manifest.ProjectID)
		}
		remoteEvents, err := client.Pull(cmd.Context(), 0)
		if err != nil {
			return mapError(err)
		}
		localEvents, err := eventstore.New(root).ReadAll(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		if !sameAcceptedHistory(localEvents, remoteEvents) {
			return mapError(fmt.Errorf("%w: local history diverges from Team Core", app.ErrConflict))
		}
		acceptedPath := filepath.Join(root, ".haowork", "team", "events.jsonl")
		if err := ensureTeamAcceptedLog(acceptedPath); err != nil {
			return mapError(err)
		}
		accepted := eventstore.NewAt(acceptedPath, filepath.Join(root, ".haowork", "team", "events.lock"))
		if _, err := accepted.ImportAcceptedBatch(cmd.Context(), remoteEvents); err != nil {
			return mapError(fmt.Errorf("import Team accepted history: %w", err))
		}
		config := teamsync.ClientConfig{Endpoint: endpoint, DeviceID: deviceID, EnvironmentID: environmentID, PrincipalID: status.Principal.Actor.ID, TeamProjectID: status.ProjectID}
		if err := teamsync.SaveConfig(cmd.Context(), root, config); err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "joined Team Core", config)
	}}
	command.Flags().StringVar(&endpoint, "endpoint", "", "Team Core endpoint")
	command.Flags().StringVar(&deviceID, "device-id", "", "local device identity")
	command.Flags().StringVar(&environmentID, "environment-id", "", "execution environment identity")
	return command
}

func ensureTeamAcceptedLog(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if file != nil {
		return file.Close()
	}
	return nil
}

func sameAcceptedHistory(local, remote []model.Event) bool { return reflect.DeepEqual(local, remote) }

func newTeamStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, remote, err := teamRemote(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		value, err := remote.Status(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "Team status", value)
	}}
}
func newTeamSyncCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "sync", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		project, err := teamProject(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		if project.Team == nil {
			return mapError(errors.New("project is not joined to a Team Core"))
		}
		value, err := project.Team.Sync.Sync(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "Team sync complete", value)
	}}
}

func newTeamLeaseCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "lease"}
	var subject, contextID, scope, skill, expiry string
	issue := &cobra.Command{Use: "issue TASK-ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, r, err := teamRemote(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		at, err := time.Parse(time.RFC3339, expiry)
		if err != nil {
			return err
		}
		v, err := r.IssueLease(cmd.Context(), model.Lease{TaskID: args[0], SubjectID: subject, ContextID: contextID, AllowedScopes: []string{scope}, AllowedSkills: []string{skill}, StartsAt: time.Now().UTC(), ExpiresAt: at})
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "lease issued", v)
	}}
	issue.Flags().StringVar(&subject, "subject", "", "subject")
	issue.Flags().StringVar(&contextID, "context-id", "", "context")
	issue.Flags().StringVar(&scope, "scope", "", "scope")
	issue.Flags().StringVar(&skill, "skill", "", "skill")
	issue.Flags().StringVar(&expiry, "expires-at", "", "expiry")
	command.AddCommand(issue)
	for _, action := range []string{"renew", "release", "revoke"} {
		a := action
		command.AddCommand(&cobra.Command{Use: a + " LEASE-ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, r, err := teamRemote(cmd.Context(), deps)
			if err != nil {
				return mapError(err)
			}
			var v team.PushResult
			if a == "renew" {
				v, err = r.RenewLease(cmd.Context(), args[0], time.Now().UTC().Add(time.Hour))
			} else if a == "release" {
				v, err = r.ReleaseLease(cmd.Context(), args[0])
			} else {
				v, err = r.RevokeLease(cmd.Context(), args[0])
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "lease "+a, v)
		}})
	}
	return command
}

func newTeamGoalCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "goal"}
	var statement, criterion, reason string
	propose := &cobra.Command{Use: "propose", RunE: func(cmd *cobra.Command, _ []string) error {
		_, r, err := teamRemote(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		v, err := r.ProposeGoalChange(cmd.Context(), model.GoalChange{Proposed: model.GoalVersion{Statement: statement, CompletionCriteria: []string{criterion}}, Reason: reason})
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "goal proposed", v)
	}}
	propose.Flags().StringVar(&statement, "statement", "", "statement")
	propose.Flags().StringVar(&criterion, "criterion", "", "criterion")
	propose.Flags().StringVar(&reason, "reason", "", "reason")
	command.AddCommand(propose)
	for _, action := range []string{"approve", "reject"} {
		a := action
		command.AddCommand(&cobra.Command{Use: a + " CHANGE-ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, r, err := teamRemote(cmd.Context(), deps)
			if err != nil {
				return mapError(err)
			}
			var v team.PushResult
			if a == "approve" {
				v, err = r.ApproveGoalChange(cmd.Context(), args[0])
			} else {
				v, err = r.RejectGoalChange(cmd.Context(), args[0], reason)
			}
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "goal "+a, v)
		}})
	}
	return command
}
func newTeamConflictCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "conflict"}
	command.AddCommand(&cobra.Command{Use: "list", RunE: func(cmd *cobra.Command, _ []string) error {
		_, r, err := teamRemote(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		v, err := r.Conflicts(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "Team conflicts", v)
	}})
	var action string
	resolve := &cobra.Command{Use: "resolve CONFLICT-ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if action != team.AcceptTeam && action != team.KeepAsProposal && action != team.ManualMerge && action != team.WithdrawLocal {
			return errors.New("invalid conflict action")
		}
		_, r, err := teamRemote(cmd.Context(), deps)
		if err != nil {
			return mapError(err)
		}
		v, err := r.ResolveConflict(cmd.Context(), args[0], action)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "conflict resolved", v)
	}}
	resolve.Flags().StringVar(&action, "action", "", "action")
	command.AddCommand(resolve)
	return command
}

func teamTokenSource(source teamapi.TokenSource) teamapi.TokenSource {
	if source != nil {
		return source
	}
	return cliEnvironmentTokenSource{}
}

type cliEnvironmentTokenSource struct{}

func (cliEnvironmentTokenSource) Token(context.Context) (string, error) {
	token := strings.TrimSpace(os.Getenv("HAOWORK_TEAM_TOKEN"))
	if token == "" {
		return "", errors.New("Team credential unavailable")
	}
	return token, nil
}

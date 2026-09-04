package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
	"github.com/spf13/cobra"
)

func NewAgentTeamsCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "mission", Short: "Inspect and issue governed AgentTeams missions"}
	command.AddCommand(newMissionStatusCommand(deps), newMissionIssueCommand(deps))
	return command
}

func newMissionStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			project, err := openProject(cmd.Context(), deps.Options.Project)
			if err != nil {
				return mapError(err)
			}
			state, err := project.Service.Status(cmd.Context())
			if err != nil {
				return mapError(err)
			}
			return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d mission(s)", len(state.Missions)), struct {
				Missions []model.MissionEnvelope `json:"missions"`
			}{Missions: missionValues(state.Missions)})
		}
		values, err := client.Missions(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d mission(s)", len(values)), struct {
			Missions []model.MissionEnvelope `json:"missions"`
		}{Missions: values})
	}}
}

func newMissionIssueCommand(deps *Dependencies) *cobra.Command {
	var task, contextID, risk, environment, policy, actorID, actorRole, buildAgent, verifyAgent string
	var criteria, scopes, skills []string
	command := &cobra.Command{Use: "issue", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		actor, err := actorFromFlags(actorID, "", actorRole)
		if err != nil {
			return err
		}
		input := localapi.MissionRequest{TaskIDs: []string{task}, ContextID: contextID, RiskLevel: risk, EnvironmentID: environment, PolicyVersion: policy, CompletionCriteria: criteria, AllowedScopes: scopes, Actor: actor, Assignments: map[model.AgentFunction]string{model.FunctionBuild: buildAgent, model.FunctionVerify: verifyAgent}}
		for _, value := range skills {
			parts := strings.SplitN(value, "@", 2)
			version := ""
			if len(parts) == 2 {
				version = parts[1]
			}
			input.Skills = append(input.Skills, model.MissionSkillGrant{Name: parts[0], Version: version})
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for mission issue")}
		}
		mission, err := client.IssueMission(cmd.Context(), input)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "issued mission "+mission.ID, mission)
	}}
	command.Flags().StringVar(&task, "task", "", "governance task ID")
	command.Flags().StringVar(&contextID, "context", "", "context slice ID")
	command.Flags().StringVar(&risk, "risk", "L1", "risk level")
	command.Flags().StringVar(&environment, "environment", "", "runtime environment ID")
	command.Flags().StringVar(&policy, "policy", "", "policy version")
	command.Flags().StringSliceVar(&criteria, "criteria", nil, "completion criteria")
	command.Flags().StringSliceVar(&scopes, "scope", nil, "allowed scope")
	command.Flags().StringSliceVar(&skills, "skill", nil, "skill[@version]")
	command.Flags().StringVar(&actorID, "actor", "", "actor ID")
	command.Flags().StringVar(&actorRole, "role", "", "actor role")
	command.Flags().StringVar(&buildAgent, "build-agent", "", "build logical agent ID")
	command.Flags().StringVar(&verifyAgent, "verify-agent", "", "verify logical agent ID")
	_ = command.MarkFlagRequired("task")
	_ = command.MarkFlagRequired("context")
	_ = command.MarkFlagRequired("actor")
	_ = command.MarkFlagRequired("build-agent")
	_ = command.MarkFlagRequired("verify-agent")
	return command
}

func NewAgentsCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "agents", Short: "Inspect and rebind logical Agent identities"}
	command.AddCommand(newAgentsStatusCommand(deps), newAgentsRebindCommand(deps))
	return command
}
func newAgentsStatusCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for Agent topology")}
		}
		value, err := client.Topology(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d logical agent(s)", len(value.Agents)), value)
	}}
}
func newAgentsRebindCommand(deps *Dependencies) *cobra.Command {
	var id, actorID, env, instance, principal, role string
	var confirmed bool
	command := &cobra.Command{Use: "rebind", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !confirmed {
			return &CodedError{Code: ExitApproval, Err: errors.New("--confirm is required for runtime rebind")}
		}
		actor, err := actorFromFlags(actorID, "", role)
		if err != nil {
			return err
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for runtime rebind")}
		}
		binding, err := client.RebindAgent(cmd.Context(), id, model.RuntimeBinding{EnvironmentID: env, AgentTeamsInstanceID: instance, RuntimePrincipalID: principal}, actor, confirmed)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "rebound "+id, binding)
	}}
	command.Flags().StringVar(&id, "agent", "", "logical agent ID")
	command.Flags().StringVar(&actorID, "actor", "", "owner actor ID")
	command.Flags().StringVar(&env, "environment", "", "runtime environment ID")
	command.Flags().StringVar(&instance, "instance", "", "AgentTeams instance ID")
	command.Flags().StringVar(&principal, "principal", "", "runtime principal ID")
	command.Flags().StringVar(&role, "role", "owner", "actor role")
	command.Flags().BoolVar(&confirmed, "confirm", false, "confirm owner rebind")
	_ = command.MarkFlagRequired("agent")
	_ = command.MarkFlagRequired("actor")
	return command
}

func NewSkillsCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "skills", Short: "Inspect the canonical skill registry"}
	command.AddCommand(newSkillsListCommand(deps), newSkillsInspectCommand(deps))
	return command
}
func newSkillsListCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for skill registry")}
		}
		values, err := client.Skills(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d skill(s)", len(values)), struct {
			Skills any `json:"skills"`
		}{values})
	}}
}
func newSkillsInspectCommand(deps *Dependencies) *cobra.Command {
	var name string
	command := newSkillsListCommand(deps)
	command.Use = "inspect"
	command.Flags().StringVar(&name, "name", "", "skill name")
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for skill registry")}
		}
		values, err := client.Skills(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		for _, value := range values {
			if name == "" || value.Name == name {
				return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, value.Name+" "+value.Version, value)
			}
		}
		return &CodedError{Code: ExitUsage, Err: errors.New("skill not found")}
	}
	return command
}

func NewTraceCommand(deps *Dependencies) *cobra.Command {
	var missionID, after string
	command := &cobra.Command{Use: "trace", Short: "Query immutable execution traces"}
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for trace query")}
		}
		value, err := client.Traces(cmd.Context(), missionID, after)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d trace(s)", len(value.Traces)), value)
	}}
	list.Flags().StringVar(&missionID, "mission", "", "mission ID")
	list.Flags().StringVar(&after, "after", "", "opaque trace sequence")
	command.AddCommand(list)
	return command
}

func NewApprovalsCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "approvals", Short: "Review hash-bound human approvals"}
	command.AddCommand(newApprovalsListCommand(deps), newApprovalsDecideCommand(deps))
	return command
}
func newApprovalsListCommand(deps *Dependencies) *cobra.Command {
	return &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for approvals")}
		}
		values, err := client.Approvals(cmd.Context())
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, fmt.Sprintf("%d approval(s)", len(values)), struct {
			Approvals any `json:"approvals"`
		}{values})
	}}
}
func newApprovalsDecideCommand(deps *Dependencies) *cobra.Command {
	var id, hash, decision, reason, actorID, role string
	var confirmed bool
	command := &cobra.Command{Use: "decide", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !confirmed {
			return &CodedError{Code: ExitApproval, Err: errors.New("--confirm is required for approval decision")}
		}
		actor, err := actorFromFlags(actorID, "", role)
		if err != nil {
			return err
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for approval decision")}
		}
		value, err := client.DecideApproval(cmd.Context(), id, hash, decision, reason, actor)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "approval "+value.Status, value)
	}}
	command.Flags().StringVar(&id, "id", "", "approval ID")
	command.Flags().StringVar(&hash, "hash", "", "payload SHA-256")
	command.Flags().StringVar(&decision, "decision", "", "approved or rejected")
	command.Flags().StringVar(&reason, "reason", "", "decision reason")
	command.Flags().StringVar(&actorID, "actor", "", "actor ID")
	command.Flags().StringVar(&role, "role", "", "actor role")
	command.Flags().BoolVar(&confirmed, "confirm", false, "confirm human decision")
	_ = command.MarkFlagRequired("id")
	_ = command.MarkFlagRequired("hash")
	_ = command.MarkFlagRequired("decision")
	_ = command.MarkFlagRequired("actor")
	return command
}

func NewTransferCommand(deps *Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "transfer", Short: "Preview and apply signed Project Capsule transfers"}
	command.AddCommand(newTransferExportCommand(deps), newTransferReturnApprovalCommand(deps), newTransferReturnCommand(deps), newTransferPreviewCommand(deps), newTransferApplyCommand(deps))
	return command
}

func newTransferReturnApprovalCommand(deps *Dependencies) *cobra.Command {
	var input, actorID string
	command := &cobra.Command{Use: "request-return-approval", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		file, err := os.Open(input)
		if err != nil {
			return operationalError(err)
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
		decoder.DisallowUnknownFields()
		var request transfer.ReturnRequest
		if err := decoder.Decode(&request); err != nil {
			return operationalError(errors.New("invalid transfer return request"))
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return operationalError(errors.New("invalid transfer return request"))
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for transfer return approval")}
		}
		approval, err := client.RequestTransferReturnApproval(cmd.Context(), request, model.Actor{ID: actorID, Kind: model.ActorAgent, Role: model.RoleAgent})
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "requested transfer return approval "+approval.ID, approval)
	}}
	command.Flags().StringVar(&input, "input", "", "transfer return request JSON file")
	command.Flags().StringVar(&actorID, "actor", "", "requesting logical Agent ID")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("actor")
	return command
}
func newTransferExportCommand(deps *Dependencies) *cobra.Command {
	var input, output string
	command := &cobra.Command{Use: "export", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := os.ReadFile(input)
		if err != nil {
			return operationalError(err)
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for transfer export")}
		}
		archive, err := client.ExportTransfer(cmd.Context(), data)
		if err != nil {
			return mapError(err)
		}
		if err := os.WriteFile(output, archive, 0o600); err != nil {
			return operationalError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "exported transfer capsule", struct {
			Output string `json:"output"`
		}{output})
	}}
	command.Flags().StringVar(&input, "input", "", "export request JSON file")
	command.Flags().StringVar(&output, "output", "", "archive output path")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("output")
	return command
}

func newTransferReturnCommand(deps *Dependencies) *cobra.Command {
	var input, output string
	command := &cobra.Command{Use: "return", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		request, err := os.ReadFile(input)
		if err != nil {
			return operationalError(err)
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for transfer return")}
		}
		archive, conflicts, err := client.BuildTransferReturn(cmd.Context(), request)
		if err != nil {
			return mapError(err)
		}
		if err := os.WriteFile(output, archive, 0o600); err != nil {
			return operationalError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "built transfer return", struct {
			Output    string   `json:"output"`
			Conflicts []string `json:"conflicts"`
		}{Output: output, Conflicts: conflicts})
	}}
	command.Flags().StringVar(&input, "input", "", "approved return request JSON file")
	command.Flags().StringVar(&output, "output", "", "return archive output path")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("output")
	return command
}

func newTransferPreviewCommand(deps *Dependencies) *cobra.Command {
	var input string
	command := &cobra.Command{Use: "preview", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := os.ReadFile(input)
		if err != nil {
			return operationalError(err)
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for transfer preview")}
		}
		value, err := client.PreviewTransfer(cmd.Context(), data)
		if err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "transfer preview "+value.PreviewHash, value)
	}}
	command.Flags().StringVar(&input, "input", "", "signed archive path")
	_ = command.MarkFlagRequired("input")
	return command
}
func newTransferApplyCommand(deps *Dependencies) *cobra.Command {
	var hash, actorID, role string
	var confirmed bool
	command := &cobra.Command{Use: "apply", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !confirmed {
			return &CodedError{Code: ExitApproval, Err: errors.New("--confirm is required for transfer apply")}
		}
		actor, err := actorFromFlags(actorID, "", role)
		if err != nil {
			return err
		}
		client := localAPIClient(cmd.Context(), deps.Options.Project)
		if client == nil {
			return &CodedError{Code: ExitOffline, Err: errors.New("local Core is required for transfer apply")}
		}
		if err := client.ApplyTransfer(cmd.Context(), hash, actor, confirmed); err != nil {
			return mapError(err)
		}
		return writeOutput(cmd.OutOrStdout(), deps.Options.JSON, "transfer applied", struct {
			PreviewHash string `json:"preview_hash"`
		}{hash})
	}}
	command.Flags().StringVar(&hash, "preview-hash", "", "preview hash")
	command.Flags().StringVar(&actorID, "actor", "", "owner actor ID")
	command.Flags().StringVar(&role, "role", "owner", "actor role")
	command.Flags().BoolVar(&confirmed, "confirm", false, "confirm owner import")
	_ = command.MarkFlagRequired("preview-hash")
	_ = command.MarkFlagRequired("actor")
	return command
}

func missionValues(values map[string]model.MissionEnvelope) []model.MissionEnvelope {
	result := make([]model.MissionEnvelope, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

var _ = context.Background
var _ = base64.StdEncoding
var _ = json.Valid

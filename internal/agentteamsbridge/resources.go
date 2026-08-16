package agentteamsbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

func RenderResources(envelope model.MissionEnvelope) ([]Resource, error) {
	if strings.TrimSpace(envelope.ID) == "" {
		return nil, fmt.Errorf("mission id is required")
	}
	for _, function := range []model.AgentFunction{model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify} {
		if strings.TrimSpace(envelope.RoleAssignments[function]) == "" {
			return nil, fmt.Errorf("mission assignment for %s is required", function)
		}
	}
	teamName := "haowork-" + strings.ToLower(strings.TrimSpace(envelope.ID))
	managerSpec, err := json.Marshal(map[string]any{"logicalActorID": envelope.RoleAssignments[model.FunctionManager], "leaderRef": "delivery-leader", "missionID": envelope.ID})
	if err != nil {
		return nil, err
	}
	teamSpec, err := json.Marshal(map[string]any{
		"missionID": envelope.ID, "peerMentions": true,
		"leader": map[string]any{"name": "delivery-leader", "logicalActorID": envelope.RoleAssignments[model.FunctionDeliveryLeader], "mcp": mcpGrant(envelope, model.FunctionDeliveryLeader)},
		"workers": []map[string]any{
			{"name": "research-worker", "logicalActorID": envelope.RoleAssignments[model.FunctionResearch], "mcp": mcpGrant(envelope, model.FunctionResearch)},
			{"name": "build-worker", "logicalActorID": envelope.RoleAssignments[model.FunctionBuild], "mcp": mcpGrant(envelope, model.FunctionBuild)},
			{"name": "verify-worker", "logicalActorID": envelope.RoleAssignments[model.FunctionVerify], "mcp": mcpGrant(envelope, model.FunctionVerify)},
		},
	})
	if err != nil {
		return nil, err
	}
	humanSpec, err := json.Marshal(map[string]any{"permission": "team", "teamRef": teamName})
	if err != nil {
		return nil, err
	}
	return []Resource{
		{APIVersion: legacyControlAPIVersion, Kind: "Manager", Name: teamName + "-manager", Spec: managerSpec},
		{APIVersion: legacyControlAPIVersion, Kind: "Team", Name: teamName, Spec: teamSpec},
		{APIVersion: legacyControlAPIVersion, Kind: "Human", Name: teamName + "-owner", Spec: humanSpec},
	}, nil
}

func mcpGrant(envelope model.MissionEnvelope, function model.AgentFunction) map[string]any {
	allowed := roleSkillAllowlist(function)
	tools := make([]string, 0, len(envelope.AllowedSkills))
	for _, grant := range envelope.AllowedSkills {
		if allowed[strings.TrimSpace(grant.Name)] {
			tools = append(tools, "haowork-mcp/"+strings.TrimSpace(grant.Name))
		}
	}
	return map[string]any{"server": "haowork-mcp", "tools": stableToolGrants(tools), "consumerTokenRef": "haowork-consumer-token"}
}

func roleSkillAllowlist(function model.AgentFunction) map[string]bool {
	switch function {
	case model.FunctionDeliveryLeader:
		return map[string]bool{"plan": true, "context": true, "record": true, "history": true}
	case model.FunctionResearch:
		return map[string]bool{"context": true, "history": true, "advisory": true, "mirror": true}
	case model.FunctionBuild:
		return map[string]bool{"context": true, "record": true, "history": true, "patch": true}
	case model.FunctionVerify:
		return map[string]bool{"history": true, "verify": true, "audit": true}
	default:
		return map[string]bool{}
	}
}

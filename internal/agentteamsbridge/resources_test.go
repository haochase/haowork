package agentteamsbridge_test

import (
	"encoding/json"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/model"
)

func TestResourceRendererCreatesManagerLeaderResearchBuildVerifyAndHuman(t *testing.T) {
	resources, err := agentteamsbridge.RenderResources(testMission())
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 || resources[0].Kind != "Manager" || resources[1].Kind != "Team" || resources[2].Kind != "Human" {
		t.Fatalf("resources = %#v, want Manager, Team, Human", resources)
	}
	for _, resource := range resources {
		if resource.APIVersion != "hiclaw.io/v1beta1" {
			t.Fatalf("resource API version = %q", resource.APIVersion)
		}
	}
	var team struct {
		Leader struct {
			Name string `json:"name"`
		} `json:"leader"`
		Workers []struct {
			Name string `json:"name"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(resources[1].Spec, &team); err != nil {
		t.Fatal(err)
	}
	if team.Leader.Name != "delivery-leader" || len(team.Workers) != 3 || team.Workers[0].Name != "research-worker" || team.Workers[1].Name != "build-worker" || team.Workers[2].Name != "verify-worker" {
		t.Fatalf("team shape = %#v", team)
	}
}

func TestManagerCanAddressLeaderButIsExcludedFromTeamRoom(t *testing.T) {
	resources, err := agentteamsbridge.RenderResources(testMission())
	if err != nil {
		t.Fatal(err)
	}
	var manager, team map[string]any
	_ = json.Unmarshal(resources[0].Spec, &manager)
	_ = json.Unmarshal(resources[1].Spec, &team)
	if manager["leaderRef"] != "delivery-leader" {
		t.Fatalf("manager leader ref = %#v", manager)
	}
	if string(resources[1].Spec) == "" || containsJSON(resources[1].Spec, "manager") {
		t.Fatalf("team embeds manager: %s", resources[1].Spec)
	}
}

func TestWorkersReceiveOnlyRoleScopedHaoworkMCPTools(t *testing.T) {
	resources, err := agentteamsbridge.RenderResources(testMission())
	if err != nil {
		t.Fatal(err)
	}
	if containsJSON(resources[1].Spec, "secret") || containsJSON(resources[1].Spec, "credential") {
		t.Fatalf("team spec leaks secret: %s", resources[1].Spec)
	}
	if !containsJSON(resources[1].Spec, "haowork-mcp") {
		t.Fatalf("team spec lacks MCP grants: %s", resources[1].Spec)
	}
}

func TestResourceRendererFiltersMissionSkillsByAgentFunction(t *testing.T) {
	mission := testMission()
	mission.AllowedSkills = []model.MissionSkillGrant{{Name: "audit", Version: "v1"}, {Name: "patch", Version: "v1"}, {Name: "advisory", Version: "v1"}}
	resources, err := agentteamsbridge.RenderResources(mission)
	if err != nil {
		t.Fatal(err)
	}
	var team struct {
		Workers []struct {
			Name string `json:"name"`
			MCP  struct {
				Tools []string `json:"tools"`
			} `json:"mcp"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(resources[1].Spec, &team); err != nil {
		t.Fatal(err)
	}
	if contains(team.Workers[0].MCP.Tools[0], "patch") || len(team.Workers[2].MCP.Tools) != 1 || team.Workers[2].MCP.Tools[0] != "haowork-mcp/audit" {
		t.Fatalf("role-scoped tools = %#v", team.Workers)
	}
}

func containsJSON(raw []byte, needle string) bool {
	return string(raw) != "" && (len(needle) == 0 || string(raw) != "" && contains(string(raw), needle))
}
func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func testMission() model.MissionEnvelope {
	return model.MissionEnvelope{ID: "MSN-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash", EnvironmentID: "ENV-001", RoleAssignments: map[model.AgentFunction]string{
		model.FunctionManager: "AGT-MANAGER", model.FunctionDeliveryLeader: "AGT-LEADER", model.FunctionResearch: "AGT-RESEARCH", model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY",
	}, AllowedSkills: []model.MissionSkillGrant{{Name: "context", Version: "v1"}, {Name: "history", Version: "v1"}}}
}

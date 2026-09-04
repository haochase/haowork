package agentteamsbridge_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderMissionResourcesUsesOfficialKindsAndFields(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatalf("render official mission resources: %v", err)
	}

	objects := resources.All()
	if len(objects) != 7 {
		t.Fatalf("resource count = %d, want Manager, four Workers, Team, and Human", len(objects))
	}
	for _, object := range objects {
		if object.GetAPIVersion() != "agentteams.io/v1beta1" {
			t.Fatalf("%s apiVersion = %q", object.GetKind(), object.GetAPIVersion())
		}
		if object.GetLabels()[agentteamsbridge.OfficialControllerOwnershipLabel] != "agentteams-public" {
			t.Fatalf("%s controller ownership = %#v", object.GetKind(), object.GetLabels())
		}
		if object.GetLabels()[agentteamsbridge.HaoworkMissionLabel] != "msn-001" || object.GetLabels()[agentteamsbridge.HaoworkEnvironmentLabel] != "public" {
			t.Fatalf("%s is missing Haowork mission/environment labels: %#v", object.GetKind(), object.GetLabels())
		}
	}

	assertOfficialManager(t, resources.Manager.Object)
	assertOfficialTeam(t, resources.Team.Object)
	assertOfficialHuman(t, resources.Human.Object)
}

func TestRenderWorkerBindsRoleSkillsAndMCPServer(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatalf("render official mission resources: %v", err)
	}

	wantSkills := map[model.AgentFunction][]string{
		model.FunctionDeliveryLeader: {"context"},
		model.FunctionResearch:       {"advisory", "context"},
		model.FunctionBuild:          {"context", "patch"},
		model.FunctionVerify:         {"audit"},
	}
	if len(resources.Workers) != len(wantSkills) {
		t.Fatalf("worker count = %d", len(resources.Workers))
	}
	for _, worker := range resources.Workers {
		function := model.AgentFunction(worker.Object.GetLabels()[agentteamsbridge.HaoworkAgentFunctionLabel])
		wantSkills, exists := wantSkills[function]
		if !exists {
			t.Fatalf("worker has unknown function %q", function)
		}
		skills, found, err := unstructured.NestedStringSlice(worker.Object.Object, "spec", "skills")
		if err != nil || !found {
			t.Fatalf("%s skills = %#v, found=%t err=%v", function, skills, found, err)
		}
		if len(skills) != len(wantSkills) {
			t.Fatalf("%s skills = %#v, want %#v", function, skills, wantSkills)
		}
		for index, wantSkill := range wantSkills {
			if skills[index] != wantSkill {
				t.Fatalf("%s skills = %#v, want %#v", function, skills, wantSkills)
			}
		}
		servers, found, err := unstructured.NestedSlice(worker.Object.Object, "spec", "mcpServers")
		if err != nil || !found || len(servers) != 1 {
			t.Fatalf("%s mcpServers = %#v, found=%t err=%v", function, servers, found, err)
		}
		server, ok := servers[0].(map[string]any)
		if !ok {
			t.Fatalf("%s mcp server = %#v", function, servers[0])
		}
		if server["name"] != "haowork-mcp" || server["url"] != "https://higress.example.test/mcp" || server["transport"] != "http" {
			t.Fatalf("%s official mcp server = %#v", function, server)
		}
		if _, hasLegacyTokenRef := server["consumerTokenRef"]; hasLegacyTokenRef {
			t.Fatalf("%s retains a private consumer token field: %#v", function, server)
		}
	}
}

func TestRenderMissionResourcesRejectsSensitiveMCPURLComponents(t *testing.T) {
	for name, endpoint := range map[string]string{
		"credential": "https://token:secret@higress.example.test/mcp",
		"query":      "https://higress.example.test/mcp?access_token=secret",
		"fragment":   "https://higress.example.test/mcp#secret",
		"scheme":     "ftp://higress.example.test/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			config := officialTestResourceConfig()
			config.MCPServerURL = endpoint

			if _, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), config); err == nil {
				t.Fatal("render accepted an MCP URL containing sensitive components")
			} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatalf("render error leaked credential material: %v", err)
			}
		})
	}
}

func TestRenderMissionResourcesUsesValidDistinctNamesForNonASCIIAndLongMissionIDs(t *testing.T) {
	first := officialTestMission()
	first.ID = "\u5b89\u5168\u8865\u4e01 / \u9996\u6b21\u4ea4\u4ed8"
	second := officialTestMission()
	second.ID = "\u5b89\u5168\u8865\u4e01---\u9996\u6b21\u4ea4\u4ed8"
	accented := officialTestMission()
	accented.ID = "caf\u00e9-security-patch"

	firstResources, err := agentteamsbridge.RenderOfficialMissionResources(first, officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondResources, err := agentteamsbridge.RenderOfficialMissionResources(second, officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	accentedResources, err := agentteamsbridge.RenderOfficialMissionResources(accented, officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if firstResources.Team.Object.GetName() == secondResources.Team.Object.GetName() {
		t.Fatalf("distinct non-ASCII mission IDs collided at team name %q", firstResources.Team.Object.GetName())
	}
	if firstResources.Team.Object.GetLabels()[agentteamsbridge.HaoworkMissionLabel] == secondResources.Team.Object.GetLabels()[agentteamsbridge.HaoworkMissionLabel] {
		t.Fatalf("distinct non-ASCII mission IDs collided at mission label %q", firstResources.Team.Object.GetLabels()[agentteamsbridge.HaoworkMissionLabel])
	}
	long := officialTestMission()
	long.ID = strings.Repeat("security-patch-delivery-", 5)
	longResources, err := agentteamsbridge.RenderOfficialMissionResources(long, officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}

	dns1123 := regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	objects := append(firstResources.All(), secondResources.All()...)
	objects = append(objects, accentedResources.All()...)
	objects = append(objects, longResources.All()...)
	for _, object := range objects {
		if len(object.GetName()) > 63 || !dns1123.MatchString(object.GetName()) {
			t.Fatalf("%s name is not a valid DNS label: %q", object.GetKind(), object.GetName())
		}
		missionLabel := object.GetLabels()[agentteamsbridge.HaoworkMissionLabel]
		if len(missionLabel) > 63 || !dns1123.MatchString(missionLabel) {
			t.Fatalf("%s mission label is not a valid Kubernetes label value: %q", object.GetKind(), missionLabel)
		}
	}
}

func assertOfficialManager(t *testing.T, object *unstructured.Unstructured) {
	t.Helper()
	if object.GetKind() != "Manager" {
		t.Fatalf("manager kind = %q", object.GetKind())
	}
	for field, want := range map[string]string{"model": "qwen3.5-plus", "runtime": "qwenpaw", "image": "registry.example.test/agentteams-manager:v1.2.2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "state": "Running"} {
		got, found, err := unstructured.NestedString(object.Object, "spec", field)
		if err != nil || !found || got != want {
			t.Fatalf("manager spec.%s = %q, found=%t err=%v, want %q", field, got, found, err, want)
		}
	}
	skills, found, err := unstructured.NestedStringSlice(object.Object, "spec", "skills")
	if err != nil || !found || len(skills) != 4 {
		t.Fatalf("manager skills = %#v, found=%t err=%v", skills, found, err)
	}
	servers, found, err := unstructured.NestedSlice(object.Object, "spec", "mcpServers")
	if err != nil || !found || len(servers) != 1 {
		t.Fatalf("manager mcpServers = %#v, found=%t err=%v", servers, found, err)
	}
}

func assertOfficialTeam(t *testing.T, object *unstructured.Unstructured) {
	t.Helper()
	if object.GetKind() != "Team" {
		t.Fatalf("team kind = %q", object.GetKind())
	}
	members, found, err := unstructured.NestedSlice(object.Object, "spec", "workerMembers")
	if err != nil || !found || len(members) != 4 {
		t.Fatalf("team workerMembers = %#v, found=%t err=%v", members, found, err)
	}
	leaders := 0
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok || member["name"] == "" || member["role"] == "" {
			t.Fatalf("team member = %#v", raw)
		}
		if member["role"] == "team_leader" {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("team leader count = %d", leaders)
	}
}

func assertOfficialHuman(t *testing.T, object *unstructured.Unstructured) {
	t.Helper()
	if object.GetKind() != "Human" {
		t.Fatalf("human kind = %q", object.GetKind())
	}
	displayName, found, err := unstructured.NestedString(object.Object, "spec", "displayName")
	if err != nil || !found || displayName != "Haowork Owner" {
		t.Fatalf("human displayName = %q, found=%t err=%v", displayName, found, err)
	}
	permission, found, err := unstructured.NestedInt64(object.Object, "spec", "permissionLevel")
	if err != nil || !found || permission != 2 {
		t.Fatalf("human permissionLevel = %d, found=%t err=%v", permission, found, err)
	}
	teams, found, err := unstructured.NestedStringSlice(object.Object, "spec", "accessibleTeams")
	if err != nil || !found || len(teams) != 1 || teams[0] == "" {
		t.Fatalf("human accessibleTeams = %#v, found=%t err=%v", teams, found, err)
	}
}

func officialTestMission() model.MissionEnvelope {
	return model.MissionEnvelope{
		ID:            "MSN-001",
		EnvironmentID: "public",
		Hash:          "a8a3a1b6120d4b86e22d8a3a08caf85e1e7de6f4cfa70b3e7dc9b25cab1c423e",
		RoleAssignments: map[model.AgentFunction]string{
			model.FunctionManager:        "AGT-MANAGER",
			model.FunctionDeliveryLeader: "AGT-LEADER",
			model.FunctionResearch:       "AGT-RESEARCH",
			model.FunctionBuild:          "AGT-BUILD",
			model.FunctionVerify:         "AGT-VERIFY",
		},
		AllowedSkills: []model.MissionSkillGrant{
			{Name: "context", Version: "v1"},
			{Name: "advisory", Version: "v1"},
			{Name: "patch", Version: "v1"},
			{Name: "audit", Version: "v1"},
		},
	}
}

func officialTestResourceConfig() agentteamsbridge.OfficialResourceConfig {
	return agentteamsbridge.OfficialResourceConfig{
		Namespace:      "haowork-public",
		ControllerName: "agentteams-public",
		Model:          "qwen3.5-plus",
		ManagerRuntime: "qwenpaw",
		ManagerImage:   "registry.example.test/agentteams-manager:v1.2.2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkerRuntime:  "openclaw",
		MCPServerName:  "haowork-mcp",
		MCPServerURL:   "https://higress.example.test/mcp",
		MCPTransport:   "http",
		HumanName:      "Haowork Owner",
	}
}

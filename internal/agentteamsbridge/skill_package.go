package agentteamsbridge

import (
	"errors"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

// WorkerSkillPackage is the content contract for a Worker skill bundle. The
// controller receives Spec directly as Worker.spec.skills and
// Worker.spec.mcpServers; the external manager synchronizes the documented
// files beneath agents/{worker}/skills/{skill}/.
type WorkerSkillPackage struct {
	WorkerName string                   `json:"worker_name"`
	Spec       WorkerSkillPackageSpec   `json:"spec"`
	Skills     []WorkerSkillDescription `json:"skills"`
}

type WorkerSkillPackageSpec struct {
	Skills     []string                   `json:"skills"`
	MCPServers []WorkerMCPServerReference `json:"mcpServers"`
}

type WorkerMCPServerReference struct {
	Name string `json:"name"`
}

// WorkerSkillDescription intentionally advertises no executor internals or
// credentials. Every ToolName remains a call through the Haowork MCP host.
type WorkerSkillDescription struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	ToolName         string `json:"tool_name"`
	MCPServerName    string `json:"mcp_server_name"`
	DistributionPath string `json:"distribution_path"`
}

func BuildWorkerSkillPackage(registry *skillruntime.Registry, workerName string, function model.AgentFunction, grants []model.MissionSkillGrant, mcpServerName string) (WorkerSkillPackage, error) {
	workerName = strings.TrimSpace(workerName)
	mcpServerName = strings.TrimSpace(mcpServerName)
	if registry == nil || workerName == "" || mcpServerName == "" {
		return WorkerSkillPackage{}, errors.New("canonical registry, worker name, and MCP server name are required")
	}
	allowed := roleSkillAllowlist(function)
	if len(allowed) == 0 {
		return WorkerSkillPackage{}, errors.New("worker function has no governed skill allowlist")
	}
	selected := make(map[string]WorkerSkillDescription)
	for _, grant := range grants {
		name, version := strings.TrimSpace(grant.Name), strings.TrimSpace(grant.Version)
		if !allowed[name] {
			continue
		}
		definition, err := registry.Resolve(name, version)
		if err != nil || definition.Name != name || definition.Version != version {
			return WorkerSkillPackage{}, errors.New("worker skill grant is not present in the canonical registry")
		}
		if !workerFunctionAllowed(definition.AllowedFunctions, function) {
			return WorkerSkillPackage{}, errors.New("canonical skill does not allow the worker function")
		}
		selected[name] = WorkerSkillDescription{
			Name: name, Version: version, ToolName: "haowork." + name, MCPServerName: mcpServerName,
			DistributionPath: "agents/" + workerName + "/skills/" + name + "/SKILL.md",
		}
	}
	if len(selected) == 0 {
		return WorkerSkillPackage{}, errors.New("mission grants no canonical skills to the worker")
	}
	skills := make([]WorkerSkillDescription, 0, len(selected))
	for _, skill := range selected {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(left, right int) bool { return skills[left].Name < skills[right].Name })
	specSkills := make([]string, 0, len(skills))
	for _, skill := range skills {
		specSkills = append(specSkills, skill.Name)
	}
	return WorkerSkillPackage{
		WorkerName: workerName,
		Spec:       WorkerSkillPackageSpec{Skills: specSkills, MCPServers: []WorkerMCPServerReference{{Name: mcpServerName}}},
		Skills:     skills,
	}, nil
}

func workerFunctionAllowed(functions []model.AgentFunction, candidate model.AgentFunction) bool {
	for _, function := range functions {
		if function == candidate {
			return true
		}
	}
	return false
}

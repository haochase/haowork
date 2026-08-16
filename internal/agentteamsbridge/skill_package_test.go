package agentteamsbridge_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

func TestSkillPackageMatchesCanonicalRegistryAndWorkerAllowlist(t *testing.T) {
	registry, err := skillruntime.Load(skillRegistryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	packageDescription, err := agentteamsbridge.BuildWorkerSkillPackage(registry, "worker-build", model.FunctionBuild, []model.MissionSkillGrant{
		{Name: "patch", Version: "1.0.0"}, {Name: "context", Version: "1.0.0"}, {Name: "audit", Version: "1.0.0"},
	}, "haowork-mcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(packageDescription.Spec.Skills) != 2 || packageDescription.Spec.Skills[0] != "context" || packageDescription.Spec.Skills[1] != "patch" {
		t.Fatalf("Worker.spec.skills = %#v", packageDescription.Spec.Skills)
	}
	if len(packageDescription.Spec.MCPServers) != 1 || packageDescription.Spec.MCPServers[0].Name != "haowork-mcp" {
		t.Fatalf("Worker.spec.mcpServers = %#v", packageDescription.Spec.MCPServers)
	}
	if len(packageDescription.Skills) != 2 || packageDescription.Skills[1].Name != "patch" || packageDescription.Skills[1].Version != "1.0.0" || packageDescription.Skills[1].ToolName != "haowork.patch" || packageDescription.Skills[1].DistributionPath != "agents/worker-build/skills/patch/SKILL.md" {
		t.Fatalf("Worker package = %#v", packageDescription)
	}
	if _, err := agentteamsbridge.BuildWorkerSkillPackage(registry, "worker-build", model.FunctionBuild, []model.MissionSkillGrant{{Name: "patch", Version: "not-a-canonical-version"}}, "haowork-mcp"); err == nil {
		t.Fatal("Worker Skill package accepted a non-canonical version")
	}
}

func skillRegistryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "skills")
}

package agentteamsbridge_test

import (
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestStableProfileRequiresOfficialV122ManagerTeamWorkerHumanAndInfrastructure(t *testing.T) {
	profile := agentteamsbridge.StableProfile()
	if profile.Name != "agentscope-ai/AgentTeams" || profile.Version != "v1.2.2" || profile.APIVersion != "agentteams.io/v1beta1" {
		t.Fatalf("stable profile = %#v", profile)
	}
	for _, kind := range []string{"Manager", "Team", "Worker", "Human"} {
		if !profile.ResourceKinds[kind] {
			t.Fatalf("stable profile does not require %s", kind)
		}
	}
	if !profile.Controller || !profile.Matrix || !profile.MinIO || !profile.HigressMCP || !profile.IsStable() {
		t.Fatalf("stable profile lacks required infrastructure: %#v", profile)
	}
}

package agentteamsbridge_test

import (
	"context"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stesting "k8s.io/client-go/testing"
)

func TestOfficialMissionOrchestratorUsesOnlyOfficialControlPlane(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	orchestrator := agentteamsbridge.OfficialMissionOrchestrator{Control: control, Config: officialTestResourceConfig()}

	if _, err := orchestrator.EnsureMissionTeam(context.Background(), officialTestMission()); err == nil {
		t.Fatal("orchestrator accepted resources before the official controller reported status")
	}

	// The first call created the CRs. The controller is the only writer of
	// readiness status, so emulate that later reconciliation before retrying.
	for _, object := range resources.All() {
		current, err := client.Resource(resourceGVR(object.GetKind())).Namespace("haowork-public").Get(context.Background(), object.GetName(), metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get created %s %s: %v", object.GetKind(), object.GetName(), err)
		}
		setObservedStatus(t, current)
		if _, err := client.Resource(resourceGVR(current.GetKind())).Namespace("haowork-public").Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("write observed %s status: %v", current.GetKind(), err)
		}
	}
	client.ClearActions()

	topology, err := orchestrator.EnsureMissionTeam(context.Background(), officialTestMission())
	if err != nil {
		t.Fatalf("ensure official mission team: %v", err)
	}
	if topology.TeamName != resources.Team.Object.GetName() || topology.ManagerPrincipalID != "@manager:example.test" {
		t.Fatalf("official topology = %#v", topology)
	}
	assertFieldManager(t, client.Actions(), agentteamsbridge.HaoworkAgentTeamsFieldManager)
	assertOfficialResourceUpdates(t, client.Actions())
	if err := orchestrator.StopMissionTeam(context.Background(), officialTestMission().ID); err != nil {
		t.Fatalf("stop official mission team: %v", err)
	}
}

func assertOfficialResourceUpdates(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	updated := map[string]bool{}
	for _, action := range actions {
		update, ok := action.(k8stesting.UpdateActionImpl)
		if !ok || update.GetResource().Group != "agentteams.io" || update.GetResource().Version != "v1beta1" {
			continue
		}
		updated[update.GetResource().Resource] = true
	}
	for _, resource := range []string{"managers", "workers", "teams", "humans"} {
		if !updated[resource] {
			t.Fatalf("official orchestrator did not update %s", resource)
		}
	}
}

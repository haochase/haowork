package agentteamsbridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/model"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamic "k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

var (
	officialCRDGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	managerGVR     = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "managers"}
	workerGVR      = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "workers"}
	teamGVR        = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "teams"}
	humanGVR       = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "humans"}
)

func TestDiscoverRequiresExactV1Beta1CRDs(t *testing.T) {
	control, _ := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	capabilities, err := control.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover official CRDs: %v", err)
	}
	if capabilities.Group != "agentteams.io" || capabilities.Version != "v1beta1" {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	for _, kind := range []string{"Manager", "Worker", "Team", "Human"} {
		if !capabilities.HasKind(kind) {
			t.Fatalf("capabilities do not include %s: %#v", kind, capabilities)
		}
	}

	missingWorker := establishedOfficialCRDs()
	missingWorker = missingWorker[:1]
	control, _ = newFakeKubernetesControlPlane(t, missingWorker...)
	if _, err := control.Discover(context.Background()); err == nil {
		t.Fatal("discover accepted missing Worker CRD")
	}

	notStorage := establishedOfficialCRDs()
	workerCRD := notStorage[1].(*unstructured.Unstructured).DeepCopy()
	versions, found, err := unstructured.NestedSlice(workerCRD.Object, "spec", "versions")
	if err != nil || !found || len(versions) != 1 {
		t.Fatalf("worker CRD versions = %#v, found=%t err=%v", versions, found, err)
	}
	versions[0].(map[string]any)["storage"] = false
	if err := unstructured.SetNestedSlice(workerCRD.Object, versions, "spec", "versions"); err != nil {
		t.Fatal(err)
	}
	notStorage[1] = workerCRD
	control, _ = newFakeKubernetesControlPlane(t, notStorage...)
	if _, err := control.Discover(context.Background()); err == nil {
		t.Fatal("discover accepted v1beta1 that is not the CRD storage version")
	}
}

func TestTopologyComesFromObservedOfficialStatus(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	seedObservedTopology(t, client, "haowork-public", resources)

	topology, err := control.GetTopology(context.Background(), resources.Team.Object.GetName())
	if err != nil {
		t.Fatalf("get observed topology: %v", err)
	}
	if topology.MissionID != "MSN-001" || topology.ManagerPrincipalID != "@manager:example.test" || topology.LeaderPrincipalID != "@leader:example.test" || topology.HumanPrincipalID != "@owner:example.test" {
		t.Fatalf("topology does not come from official status: %#v", topology)
	}
	if topology.ManagerRoomID != "!manager:example.test" || topology.LeaderRoomID != "!leader-dm:example.test" || topology.TeamRoomID != "!team:example.test" {
		t.Fatalf("topology rooms = %#v", topology)
	}
	if topology.WorkerPrincipalIDs[model.FunctionResearch] != "@research:example.test" || topology.WorkerPrincipalIDs[model.FunctionBuild] != "@build:example.test" || topology.WorkerPrincipalIDs[model.FunctionVerify] != "@verify:example.test" {
		t.Fatalf("worker principals = %#v", topology.WorkerPrincipalIDs)
	}

	team, err := client.Resource(teamGVR).Namespace("haowork-public").Get(context.Background(), resources.Team.Object.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(team.Object, false, "status", "leaderReady"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(teamGVR).Namespace("haowork-public").Update(context.Background(), team, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.GetTopology(context.Background(), resources.Team.Object.GetName()); err == nil {
		t.Fatal("topology accepted a Team whose observed leader is not ready")
	}
}

func TestTopologyRejectsResourcesWithDifferentMissionHash(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	seedObservedTopology(t, client, "haowork-public", resources)

	workerName := resources.Team.Object.GetName() + "-research"
	worker, err := client.Resource(workerGVR).Namespace("haowork-public").Get(context.Background(), workerName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotations := worker.GetAnnotations()
	annotations[agentteamsbridge.HaoworkMissionHashAnnotation] = strings.Repeat("b", 64)
	worker.SetAnnotations(annotations)
	if _, err := client.Resource(workerGVR).Namespace("haowork-public").Update(context.Background(), worker, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := control.GetTopology(context.Background(), resources.Team.Object.GetName()); err == nil {
		t.Fatal("topology accepted a Worker with a different mission hash")
	}
}

func TestTopologyRejectsForgedTeamNameWithMatchingMissionMetadata(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	forged := forgedMissionResources(t, resources, "haowork-forged-team")
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	seedObservedTopology(t, client, "haowork-public", forged)

	if _, err := control.GetTopology(context.Background(), forged.Team.Object.GetName()); err == nil {
		t.Fatal("topology accepted a forged Team name with matching controller and mission metadata")
	}
}

func TestTopologyRejectsTeamWithForgedWorkerReferences(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	seedObservedTopology(t, client, "haowork-public", resources)

	team, err := client.Resource(teamGVR).Namespace("haowork-public").Get(context.Background(), resources.Team.Object.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	members, found, err := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
	if err != nil || !found || len(members) != 4 {
		t.Fatalf("get Team worker members: members=%#v found=%t err=%v", members, found, err)
	}
	forgedMember := members[0].(map[string]any)
	forgedMember["name"] = "haowork-forged-team-delivery-leader"
	members[0] = forgedMember
	if err := unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(teamGVR).Namespace("haowork-public").Update(context.Background(), team, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := control.GetTopology(context.Background(), resources.Team.Object.GetName()); err == nil {
		t.Fatal("topology accepted a Team with forged worker references")
	}
}

func TestTopologyRejectsTeamWithForgedHumanMatrixIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*unstructured.Unstructured) error{
		"admin": func(team *unstructured.Unstructured) error {
			return unstructured.SetNestedField(team.Object, "@attacker:example.test", "spec", "admin", "matrixUserId")
		},
		"member": func(team *unstructured.Unstructured) error {
			members, found, err := unstructured.NestedSlice(team.Object, "spec", "humanMembers")
			if err != nil || !found || len(members) != 1 {
				return fmt.Errorf("human members=%#v found=%t err=%w", members, found, err)
			}
			member, ok := members[0].(map[string]any)
			if !ok {
				return fmt.Errorf("human member=%#v", members[0])
			}
			member["matrixUserId"] = "@attacker:example.test"
			members[0] = member
			return unstructured.SetNestedSlice(team.Object, members, "spec", "humanMembers")
		},
	} {
		t.Run(name, func(t *testing.T) {
			resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
			if err != nil {
				t.Fatal(err)
			}
			control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
			seedObservedTopology(t, client, "haowork-public", resources)

			team, err := client.Resource(teamGVR).Namespace("haowork-public").Get(context.Background(), resources.Team.Object.GetName(), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := mutate(team); err != nil {
				t.Fatal(err)
			}
			if _, err := client.Resource(teamGVR).Namespace("haowork-public").Update(context.Background(), team, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}

			if _, err := control.GetTopology(context.Background(), resources.Team.Object.GetName()); err == nil {
				t.Fatal("topology accepted a forged Human Matrix identity in Team spec")
			}
		})
	}
}

func TestTopologyRejectsForgedRuntimeIdentitySpecsBeforeStatus(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface){
		"delivery leader workerName override": func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface) {
			setWorkerRuntimeName(t, client, resources.Team.Object.GetName()+"-delivery-leader", "attacker-leader")
		},
		"research workerName override": func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface) {
			setWorkerRuntimeName(t, client, resources.Team.Object.GetName()+"-research", "attacker-research")
		},
		"workerName empty": func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface) {
			setWorkerRuntimeName(t, client, resources.Team.Object.GetName()+"-research", "")
		},
		"human username override": func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface) {
			setHumanUsername(t, client, resources.Team.Object.GetName()+"-owner", "attacker-owner")
		},
		"human username empty": func(t *testing.T, resources agentteamsbridge.OfficialMissionResources, client dynamic.Interface) {
			setHumanUsername(t, client, resources.Team.Object.GetName()+"-owner", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
			if err != nil {
				t.Fatal(err)
			}
			control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
			seedMissionResourcesWithoutStatus(t, client, "haowork-public", resources)
			mutate(t, resources, client)

			_, err = control.GetTopology(context.Background(), resources.Team.Object.GetName())
			if err == nil {
				t.Fatal("topology accepted a forged official runtime identity field")
			}
			if !errors.Is(err, agentteamsbridge.ErrForeignControllerOwner) {
				t.Fatalf("topology error = %v, want runtime identity rejection before status readiness", err)
			}
		})
	}
}

func TestApplyIsIdempotentAndRejectsForeignControllerOwnership(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatalf("first manager apply: %v", err)
	}
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatalf("idempotent manager apply: %v", err)
	}
	assertFieldManager(t, client.Actions(), agentteamsbridge.HaoworkAgentTeamsFieldManager)

	foreign := resources.Manager.Object.DeepCopy()
	foreign.SetName("foreign-manager")
	foreign.SetLabels(map[string]string{agentteamsbridge.OfficialControllerOwnershipLabel: "another-controller", agentteamsbridge.HaoworkMissionLabel: "msn-001", agentteamsbridge.HaoworkEnvironmentLabel: "public"})
	foreign.SetAnnotations(map[string]string{agentteamsbridge.HaoworkMissionHashAnnotation: officialTestMission().Hash, agentteamsbridge.HaoworkMissionIDAnnotation: officialTestMission().ID})
	if _, err := client.Resource(managerGVR).Namespace("haowork-public").Create(context.Background(), foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	foreignApply := resources.Manager
	foreignApply.Object = foreign.DeepCopy()
	if _, err := control.ApplyManager(context.Background(), foreignApply); err == nil {
		t.Fatal("apply accepted an object owned by a different AgentTeams controller")
	}
}

func TestApplyRejectsCallerSuppliedRuntimeStatus(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	manager := resources.Manager
	manager.Object = manager.Object.DeepCopy()
	manager.Object.Object["status"] = map[string]any{
		"phase":          "Running",
		"matrixUserID":   "@forged:example.test",
		"roomID":         "!forged:example.test",
		"containerState": "Running",
	}

	if _, err := control.ApplyManager(context.Background(), manager); err == nil {
		t.Fatal("apply accepted caller-supplied runtime status")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "create" || action.GetVerb() == "update" || action.GetVerb() == "patch" {
			t.Fatalf("status-forging apply wrote a Kubernetes resource: %#v", action)
		}
	}
}

func TestApplyPreservesControllerMetadataAndObservedStatus(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatal(err)
	}

	current, err := client.Resource(managerGVR).Namespace("haowork-public").Get(context.Background(), resources.Manager.Object.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current.SetFinalizers([]string{"agentteams.io/cleanup"})
	annotations := current.GetAnnotations()
	annotations["agentteams.io/controller-observation"] = "retained"
	current.SetAnnotations(annotations)
	labels := current.GetLabels()
	labels["agentteams.io/controller-observation"] = "retained"
	current.SetLabels(labels)
	current.Object["status"] = map[string]any{"phase": "Running", "matrixUserID": "@manager:example.test", "roomID": "!manager:example.test"}
	if _, err := client.Resource(managerGVR).Namespace("haowork-public").Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatalf("idempotent manager apply: %v", err)
	}
	updated, err := client.Resource(managerGVR).Namespace("haowork-public").Get(context.Background(), resources.Manager.Object.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.GetFinalizers()) != 1 || updated.GetFinalizers()[0] != "agentteams.io/cleanup" {
		t.Fatalf("finalizers = %#v, want controller finalizer retained", updated.GetFinalizers())
	}
	if updated.GetAnnotations()["agentteams.io/controller-observation"] != "retained" {
		t.Fatalf("controller annotation was not retained: %#v", updated.GetAnnotations())
	}
	if updated.GetLabels()["agentteams.io/controller-observation"] != "retained" {
		t.Fatalf("controller label was not retained: %#v", updated.GetLabels())
	}
	phase, found, err := unstructured.NestedString(updated.Object, "status", "phase")
	if err != nil || !found || phase != "Running" {
		t.Fatalf("observed status phase = %q, found=%t err=%v", phase, found, err)
	}
}

func TestStopSetsOfficialDesiredStateAndWaitsForObservation(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	control.PollInterval = time.Millisecond
	control.StopObservationTimeout = time.Second
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatal(err)
	}
	for _, worker := range resources.Workers {
		if _, err := control.ApplyWorker(context.Background(), worker); err != nil {
			t.Fatal(err)
		}
	}
	if err := control.StopMissionTeam(context.Background(), officialTestMission().ID); err != nil {
		t.Fatalf("stop mission team: %v", err)
	}
	for _, gvr := range []schema.GroupVersionResource{managerGVR, workerGVR} {
		list, err := client.Resource(gvr).Namespace("haowork-public").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range list.Items {
			state, found, err := unstructured.NestedString(object.Object, "spec", "state")
			if err != nil || !found || state != "Stopped" {
				t.Fatalf("%s %s state = %q, found=%t err=%v", object.GetKind(), object.GetName(), state, found, err)
			}
		}
	}
}

func TestStopRejectsMixedMissionHashesBeforeChangingDesiredState(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatal(err)
	}
	for _, worker := range resources.Workers {
		if _, err := control.ApplyWorker(context.Background(), worker); err != nil {
			t.Fatal(err)
		}
	}

	workerName := resources.Team.Object.GetName() + "-verify"
	worker, err := client.Resource(workerGVR).Namespace("haowork-public").Get(context.Background(), workerName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	annotations := worker.GetAnnotations()
	annotations[agentteamsbridge.HaoworkMissionHashAnnotation] = strings.Repeat("c", 64)
	worker.SetAnnotations(annotations)
	if _, err := client.Resource(workerGVR).Namespace("haowork-public").Update(context.Background(), worker, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := control.StopMissionTeam(context.Background(), officialTestMission().ID); err == nil {
		t.Fatal("stop accepted Manager and Workers with different mission hashes")
	}
	for _, gvr := range []schema.GroupVersionResource{managerGVR, workerGVR} {
		list, err := client.Resource(gvr).Namespace("haowork-public").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range list.Items {
			state, found, err := unstructured.NestedString(object.Object, "spec", "state")
			if err != nil || !found || state != "Running" {
				t.Fatalf("%s %s state = %q, found=%t err=%v, want unchanged Running", object.GetKind(), object.GetName(), state, found, err)
			}
		}
	}
}

func TestStopRejectsUnexpectedMissionResourceNameBeforeChangingDesiredState(t *testing.T) {
	resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
	if err != nil {
		t.Fatal(err)
	}
	control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
	if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
		t.Fatal(err)
	}
	for _, worker := range resources.Workers {
		if _, err := control.ApplyWorker(context.Background(), worker); err != nil {
			t.Fatal(err)
		}
	}

	verifyName := resources.Team.Object.GetName() + "-verify"
	if err := client.Resource(workerGVR).Namespace("haowork-public").Delete(context.Background(), verifyName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	unexpected := resources.Workers[3].Object.DeepCopy()
	unexpected.SetName(resources.Team.Object.GetName() + "-unexpected")
	if _, err := client.Resource(workerGVR).Namespace("haowork-public").Create(context.Background(), unexpected, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := control.StopMissionTeam(context.Background(), officialTestMission().ID); err == nil {
		t.Fatal("stop accepted an unexpected Worker name")
	}
	assertMissionResourcesRemainRunning(t, client)
}

func TestStopRejectsWorkerRuntimeIdentityDriftBeforeChangingDesiredState(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "overridden worker name", value: "attacker-worker"},
		{name: "empty worker name", value: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
			if err != nil {
				t.Fatal(err)
			}
			control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
			if _, err := control.ApplyManager(context.Background(), resources.Manager); err != nil {
				t.Fatal(err)
			}
			for _, worker := range resources.Workers {
				if _, err := control.ApplyWorker(context.Background(), worker); err != nil {
					t.Fatal(err)
				}
			}

			setWorkerRuntimeName(t, client, resources.Team.Object.GetName()+"-research", test.value)
			client.ClearActions()

			err = control.StopMissionTeam(context.Background(), officialTestMission().ID)
			if !errors.Is(err, agentteamsbridge.ErrForeignControllerOwner) {
				t.Fatalf("stop error = %v, want Worker runtime identity rejection", err)
			}
			assertMissionResourcesRemainRunning(t, client)
			assertNoKubernetesWrites(t, client.Actions())
		})
	}
}

func TestApplyRejectsForgedOfficialResourceShapesBeforeWrite(t *testing.T) {
	type applyCase struct {
		name   string
		mutate func(*agentteamsbridge.OfficialMissionResources)
		apply  func(context.Context, *agentteamsbridge.KubernetesControlPlane, agentteamsbridge.OfficialMissionResources) error
	}
	tests := []applyCase{
		{
			name: "Worker workerName override",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Workers[0].Object = resources.Workers[0].Object.DeepCopy()
				if err := unstructured.SetNestedField(resources.Workers[0].Object.Object, "attacker-worker", "spec", "workerName"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyWorker(ctx, resources.Workers[0])
				return err
			},
		},
		{
			name: "Worker workerName empty",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Workers[0].Object = resources.Workers[0].Object.DeepCopy()
				if err := unstructured.SetNestedField(resources.Workers[0].Object.Object, "", "spec", "workerName"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyWorker(ctx, resources.Workers[0])
				return err
			},
		},
		{
			name: "Human username override",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Human.Object = resources.Human.Object.DeepCopy()
				if err := unstructured.SetNestedField(resources.Human.Object.Object, "attacker-owner", "spec", "username"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyHuman(ctx, resources.Human)
				return err
			},
		},
		{
			name: "Human username empty",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Human.Object = resources.Human.Object.DeepCopy()
				if err := unstructured.SetNestedField(resources.Human.Object.Object, "", "spec", "username"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyHuman(ctx, resources.Human)
				return err
			},
		},
		{
			name: "Team spec teamName override",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Team.Object = resources.Team.Object.DeepCopy()
				if err := unstructured.SetNestedField(resources.Team.Object.Object, "attacker-team", "spec", "teamName"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyTeam(ctx, resources.Team)
				return err
			},
		},
		{
			name: "Team worker reference override",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Team.Object = resources.Team.Object.DeepCopy()
				members, found, err := unstructured.NestedSlice(resources.Team.Object.Object, "spec", "workerMembers")
				if err != nil || !found {
					t.Fatalf("read worker members: found=%t err=%v", found, err)
				}
				members[0].(map[string]any)["name"] = "attacker-worker"
				if err := unstructured.SetNestedSlice(resources.Team.Object.Object, members, "spec", "workerMembers"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyTeam(ctx, resources.Team)
				return err
			},
		},
		{
			name: "Team admin reference override",
			mutate: func(resources *agentteamsbridge.OfficialMissionResources) {
				resources.Team.Object = resources.Team.Object.DeepCopy()
				if err := unstructured.SetNestedMap(resources.Team.Object.Object, map[string]any{"name": "attacker-owner"}, "spec", "admin"); err != nil {
					t.Fatal(err)
				}
			},
			apply: func(ctx context.Context, control *agentteamsbridge.KubernetesControlPlane, resources agentteamsbridge.OfficialMissionResources) error {
				_, err := control.ApplyTeam(ctx, resources.Team)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources, err := agentteamsbridge.RenderOfficialMissionResources(officialTestMission(), officialTestResourceConfig())
			if err != nil {
				t.Fatal(err)
			}
			control, client := newFakeKubernetesControlPlane(t, establishedOfficialCRDs()...)
			test.mutate(&resources)

			err = test.apply(context.Background(), control, resources)
			if !errors.Is(err, agentteamsbridge.ErrForeignControllerOwner) {
				t.Fatalf("apply error = %v, want canonical resource shape rejection", err)
			}
			assertNoKubernetesWrites(t, client.Actions())
		})
	}
}

func TestNoLegacyHiClawEndpointIsCalled(t *testing.T) {
	legacyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/v1/") || strings.Contains(request.URL.Path, "hiclaw.io") {
			legacyCalls++
			writer.WriteHeader(http.StatusTeapot)
			return
		}
		switch request.URL.Path {
		case "/apis/agentteams.io/v1beta1":
			writeJSON(t, writer, metav1.APIResourceList{GroupVersion: "agentteams.io/v1beta1", APIResources: officialAPIResources()})
		default:
			if strings.HasPrefix(request.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/") {
				name := strings.TrimPrefix(request.URL.Path, "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/")
				kind, ok := officialCRDKind(name)
				if !ok {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				writeJSON(t, writer, establishedCRD(name, kind))
				return
			}
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	control, err := agentteamsbridge.NewKubernetesControlPlaneForConfig(&rest.Config{Host: server.URL}, "haowork-public", "agentteams-public")
	if err != nil {
		t.Fatalf("construct Kubernetes control plane: %v", err)
	}
	if _, err := control.Discover(context.Background()); err != nil {
		t.Fatalf("discover through Kubernetes APIs: %v", err)
	}
	if legacyCalls != 0 {
		t.Fatalf("Kubernetes control plane called %d legacy HiClaw endpoints", legacyCalls)
	}
}

func newFakeKubernetesControlPlane(t *testing.T, objects ...runtime.Object) (*agentteamsbridge.KubernetesControlPlane, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		managerGVR: "ManagerList",
		workerGVR:  "WorkerList",
		teamGVR:    "TeamList",
		humanGVR:   "HumanList",
	}, objects...)
	discovery := &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{Resources: []*metav1.APIResourceList{{GroupVersion: "agentteams.io/v1beta1", APIResources: officialAPIResources()}}}}
	return agentteamsbridge.NewKubernetesControlPlane(client, discovery, "haowork-public", "agentteams-public"), client
}

func establishedOfficialCRDs() []runtime.Object {
	return []runtime.Object{
		establishedCRD("managers.agentteams.io", "Manager"),
		establishedCRD("workers.agentteams.io", "Worker"),
		establishedCRD("teams.agentteams.io", "Team"),
		establishedCRD("humans.agentteams.io", "Human"),
	}
}

func establishedCRD(name, kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"group":    "agentteams.io",
			"names":    map[string]any{"kind": kind, "plural": strings.ToLower(kind) + "s"},
			"versions": []any{map[string]any{"name": "v1beta1", "served": true, "storage": true}},
		},
		"status": map[string]any{"conditions": []any{map[string]any{"type": string(apiextensionsv1.Established), "status": "True"}}},
	}}
}

func officialAPIResources() []metav1.APIResource {
	return []metav1.APIResource{
		{Name: "managers", Kind: "Manager", Namespaced: true},
		{Name: "workers", Kind: "Worker", Namespaced: true},
		{Name: "teams", Kind: "Team", Namespaced: true},
		{Name: "humans", Kind: "Human", Namespaced: true},
	}
}

func officialCRDKind(name string) (string, bool) {
	kinds := map[string]string{"managers.agentteams.io": "Manager", "workers.agentteams.io": "Worker", "teams.agentteams.io": "Team", "humans.agentteams.io": "Human"}
	kind, ok := kinds[name]
	return kind, ok
}

func seedObservedTopology(t *testing.T, client dynamic.Interface, namespace string, resources agentteamsbridge.OfficialMissionResources) {
	t.Helper()
	for _, object := range resources.All() {
		copy := object.DeepCopy()
		setObservedStatus(t, copy)
		if _, err := client.Resource(resourceGVR(copy.GetKind())).Namespace(namespace).Create(context.Background(), copy, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create %s %s: %v", copy.GetKind(), copy.GetName(), err)
		}
	}
}

func seedMissionResourcesWithoutStatus(t *testing.T, client dynamic.Interface, namespace string, resources agentteamsbridge.OfficialMissionResources) {
	t.Helper()
	for _, object := range resources.All() {
		copy := object.DeepCopy()
		if _, err := client.Resource(resourceGVR(copy.GetKind())).Namespace(namespace).Create(context.Background(), copy, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create %s %s: %v", copy.GetKind(), copy.GetName(), err)
		}
	}
}

func setWorkerRuntimeName(t *testing.T, client dynamic.Interface, name, value string) {
	t.Helper()
	worker, err := client.Resource(workerGVR).Namespace("haowork-public").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(worker.Object, value, "spec", "workerName"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(workerGVR).Namespace("haowork-public").Update(context.Background(), worker, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func setHumanUsername(t *testing.T, client dynamic.Interface, name, value string) {
	t.Helper()
	human, err := client.Resource(humanGVR).Namespace("haowork-public").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(human.Object, value, "spec", "username"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(humanGVR).Namespace("haowork-public").Update(context.Background(), human, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func forgedMissionResources(t *testing.T, resources agentteamsbridge.OfficialMissionResources, teamName string) agentteamsbridge.OfficialMissionResources {
	t.Helper()
	forged := agentteamsbridge.OfficialMissionResources{
		Manager: agentteamsbridge.OfficialManager{Object: resources.Manager.Object.DeepCopy()},
		Workers: make([]agentteamsbridge.OfficialWorker, 0, len(resources.Workers)),
		Team:    agentteamsbridge.OfficialTeam{Object: resources.Team.Object.DeepCopy()},
		Human:   agentteamsbridge.OfficialHuman{Object: resources.Human.Object.DeepCopy()},
	}
	forged.Manager.Object.SetName(teamName + "-manager")
	forged.Human.Object.SetName(teamName + "-owner")
	if err := unstructured.SetNestedField(forged.Human.Object.Object, teamName+"-owner", "spec", "username"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(forged.Human.Object.Object, []any{teamName}, "spec", "accessibleTeams"); err != nil {
		t.Fatal(err)
	}

	members := make([]any, 0, len(resources.Workers))
	for _, worker := range resources.Workers {
		copy := worker.Object.DeepCopy()
		function := copy.GetLabels()[agentteamsbridge.HaoworkAgentFunctionLabel]
		name := teamName + "-" + strings.ReplaceAll(function, "_", "-")
		copy.SetName(name)
		if err := unstructured.SetNestedField(copy.Object, name, "spec", "workerName"); err != nil {
			t.Fatal(err)
		}
		forged.Workers = append(forged.Workers, agentteamsbridge.OfficialWorker{Object: copy})
		role := "worker"
		if function == "delivery_leader" {
			role = "team_leader"
		}
		members = append(members, map[string]any{"name": name, "role": role})
	}
	forged.Team.Object.SetName(teamName)
	if err := unstructured.SetNestedField(forged.Team.Object.Object, teamName, "spec", "teamName"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(forged.Team.Object.Object, members, "spec", "workerMembers"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(forged.Team.Object.Object, []any{map[string]any{"name": teamName + "-owner", "role": "coordinator"}}, "spec", "humanMembers"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedMap(forged.Team.Object.Object, map[string]any{"name": teamName + "-owner"}, "spec", "admin"); err != nil {
		t.Fatal(err)
	}
	return forged
}

func setObservedStatus(t *testing.T, object *unstructured.Unstructured) {
	t.Helper()
	switch object.GetKind() {
	case "Manager":
		object.Object["status"] = map[string]any{"phase": "Running", "matrixUserID": "@manager:example.test", "roomID": "!manager:example.test", "containerState": "Running"}
	case "Worker":
		function := object.GetLabels()[agentteamsbridge.HaoworkAgentFunctionLabel]
		principal := map[string]string{"delivery_leader": "@leader:example.test", "research": "@research:example.test", "build": "@build:example.test", "verify": "@verify:example.test"}[function]
		object.Object["status"] = map[string]any{"phase": "Running", "matrixUserID": principal, "roomID": "!" + function + ":example.test", "containerState": "Running"}
	case "Team":
		object.Object["status"] = map[string]any{
			"phase": "Active", "teamRoomID": "!team:example.test", "leaderDMRoomID": "!leader-dm:example.test", "leaderReady": true, "readyWorkers": int64(3), "totalWorkers": int64(3),
			"members": []any{
				map[string]any{"name": object.GetName() + "-delivery-leader", "role": "team_leader", "matrixUserID": "@leader:example.test", "roomID": "!delivery_leader:example.test", "observed": true, "ready": true, "phase": "Running"},
				map[string]any{"name": object.GetName() + "-research", "role": "worker", "matrixUserID": "@research:example.test", "roomID": "!research:example.test", "observed": true, "ready": true, "phase": "Running"},
				map[string]any{"name": object.GetName() + "-build", "role": "worker", "matrixUserID": "@build:example.test", "roomID": "!build:example.test", "observed": true, "ready": true, "phase": "Running"},
				map[string]any{"name": object.GetName() + "-verify", "role": "worker", "matrixUserID": "@verify:example.test", "roomID": "!verify:example.test", "observed": true, "ready": true, "phase": "Running"},
			},
		}
	case "Human":
		object.Object["status"] = map[string]any{"phase": "Active", "matrixUserID": "@owner:example.test"}
	default:
		t.Fatalf("unexpected kind %q", object.GetKind())
	}
}

func resourceGVR(kind string) schema.GroupVersionResource {
	switch kind {
	case "Manager":
		return managerGVR
	case "Worker":
		return workerGVR
	case "Team":
		return teamGVR
	case "Human":
		return humanGVR
	default:
		panic(fmt.Sprintf("unsupported kind %q", kind))
	}
}

func assertFieldManager(t *testing.T, actions []k8stesting.Action, want string) {
	t.Helper()
	foundWrite := false
	for _, action := range actions {
		switch write := action.(type) {
		case k8stesting.CreateActionImpl:
			foundWrite = true
			if write.GetCreateOptions().FieldManager != want {
				t.Fatalf("create field manager = %q, want %q", write.GetCreateOptions().FieldManager, want)
			}
		case k8stesting.UpdateActionImpl:
			foundWrite = true
			if write.GetUpdateOptions().FieldManager != want {
				t.Fatalf("update field manager = %q, want %q", write.GetUpdateOptions().FieldManager, want)
			}
		}
	}
	if !foundWrite {
		t.Fatal("apply did not issue a Kubernetes create or update action")
	}
}

func assertMissionResourcesRemainRunning(t *testing.T, client dynamic.Interface) {
	t.Helper()
	for _, gvr := range []schema.GroupVersionResource{managerGVR, workerGVR} {
		list, err := client.Resource(gvr).Namespace("haowork-public").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range list.Items {
			state, found, err := unstructured.NestedString(object.Object, "spec", "state")
			if err != nil || !found || state != "Running" {
				t.Fatalf("%s %s state = %q, found=%t err=%v, want unchanged Running", object.GetKind(), object.GetName(), state, found, err)
			}
		}
	}
}

func assertNoKubernetesWrites(t *testing.T, actions []k8stesting.Action) {
	t.Helper()
	for _, action := range actions {
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
			t.Fatalf("unexpected Kubernetes write: %#v", action)
		}
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("write JSON: %v", err)
	}
}

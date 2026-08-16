package agentteamsbridge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var (
	ErrOfficialCRDUnavailable = errors.New("required AgentTeams v1beta1 CRD is unavailable")
	ErrForeignControllerOwner = errors.New("AgentTeams resource belongs to a different controller")
	ErrOfficialStatusPending  = errors.New("AgentTeams official status is not ready")
)

var (
	customResourceDefinitionGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	officialManagerGVR          = schema.GroupVersionResource{Group: OfficialAPIGroup, Version: OfficialAPIVersion, Resource: "managers"}
	officialWorkerGVR           = schema.GroupVersionResource{Group: OfficialAPIGroup, Version: OfficialAPIVersion, Resource: "workers"}
	officialTeamGVR             = schema.GroupVersionResource{Group: OfficialAPIGroup, Version: OfficialAPIVersion, Resource: "teams"}
	officialHumanGVR            = schema.GroupVersionResource{Group: OfficialAPIGroup, Version: OfficialAPIVersion, Resource: "humans"}
)

type OfficialCapabilities struct {
	Group   string
	Version string
	Kinds   map[string]bool
}

func (capabilities OfficialCapabilities) HasKind(kind string) bool {
	return capabilities.Kinds[kind]
}

// OfficialControlPlane is the production-facing AgentTeams control contract.
// It intentionally exposes Kubernetes resources and observed status rather
// than the pre-v1.2.2 private REST capability endpoint.
type OfficialControlPlane interface {
	Discover(context.Context) (OfficialCapabilities, error)
	ApplyManager(context.Context, OfficialManager) (ResourceStatus, error)
	ApplyWorker(context.Context, OfficialWorker) (ResourceStatus, error)
	ApplyTeam(context.Context, OfficialTeam) (ResourceStatus, error)
	ApplyHuman(context.Context, OfficialHuman) (ResourceStatus, error)
	GetTopology(context.Context, string) (RuntimeTopology, error)
	StopMissionTeam(context.Context, string) error
}

// KubernetesControlPlane talks to the AgentTeams Kubernetes API directly.
// It does not execute kubectl and never calls the retired HiClaw REST routes.
type KubernetesControlPlane struct {
	Dynamic                dynamic.Interface
	Discovery              discovery.DiscoveryInterface
	Namespace              string
	ControllerName         string
	PollInterval           time.Duration
	StopObservationTimeout time.Duration
}

func NewKubernetesControlPlane(client dynamic.Interface, discoveryClient discovery.DiscoveryInterface, namespace, controllerName string) *KubernetesControlPlane {
	return &KubernetesControlPlane{
		Dynamic:                client,
		Discovery:              discoveryClient,
		Namespace:              strings.TrimSpace(namespace),
		ControllerName:         strings.TrimSpace(controllerName),
		PollInterval:           250 * time.Millisecond,
		StopObservationTimeout: 30 * time.Second,
	}
}

func NewKubernetesControlPlaneForConfig(config *rest.Config, namespace, controllerName string) (*KubernetesControlPlane, error) {
	if config == nil || strings.TrimSpace(config.Host) == "" {
		return nil, fmt.Errorf("Kubernetes REST config is required")
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes dynamic client: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes discovery client: %w", err)
	}
	return NewKubernetesControlPlane(dynamicClient, discoveryClient, namespace, controllerName), nil
}

func (control *KubernetesControlPlane) Discover(ctx context.Context) (OfficialCapabilities, error) {
	if err := control.validateClient(); err != nil {
		return OfficialCapabilities{}, err
	}
	resources, err := control.Discovery.ServerResourcesForGroupVersion(OfficialAPIGroup + "/" + OfficialAPIVersion)
	if err != nil {
		return OfficialCapabilities{}, fmt.Errorf("discover %s/%s: %w", OfficialAPIGroup, OfficialAPIVersion, err)
	}
	if resources == nil || resources.GroupVersion != OfficialAPIGroup+"/"+OfficialAPIVersion {
		return OfficialCapabilities{}, fmt.Errorf("%w: discovery returned a different group/version", ErrOfficialCRDUnavailable)
	}
	kinds := make(map[string]bool, len(officialResourceDefinitions()))
	for _, resource := range resources.APIResources {
		for _, definition := range officialResourceDefinitions() {
			if resource.Name == definition.Resource && resource.Kind == definition.Kind && resource.Namespaced {
				kinds[definition.Kind] = true
			}
		}
	}
	for _, definition := range officialResourceDefinitions() {
		if !kinds[definition.Kind] {
			return OfficialCapabilities{}, fmt.Errorf("%w: %s.%s is not served as %s", ErrOfficialCRDUnavailable, definition.Resource, OfficialAPIGroup, definition.Kind)
		}
		if err := control.validateCRD(ctx, definition); err != nil {
			return OfficialCapabilities{}, err
		}
	}
	return OfficialCapabilities{Group: OfficialAPIGroup, Version: OfficialAPIVersion, Kinds: kinds}, nil
}

func (control *KubernetesControlPlane) ApplyManager(ctx context.Context, manager OfficialManager) (ResourceStatus, error) {
	object, err := control.apply(ctx, officialManagerGVR, "Manager", manager.Object)
	if err != nil {
		return ResourceStatus{}, err
	}
	return resourceStatus(object), nil
}

func (control *KubernetesControlPlane) ApplyWorker(ctx context.Context, worker OfficialWorker) (ResourceStatus, error) {
	object, err := control.apply(ctx, officialWorkerGVR, "Worker", worker.Object)
	if err != nil {
		return ResourceStatus{}, err
	}
	return resourceStatus(object), nil
}

func (control *KubernetesControlPlane) ApplyTeam(ctx context.Context, team OfficialTeam) (ResourceStatus, error) {
	object, err := control.apply(ctx, officialTeamGVR, "Team", team.Object)
	if err != nil {
		return ResourceStatus{}, err
	}
	return resourceStatus(object), nil
}

func (control *KubernetesControlPlane) ApplyHuman(ctx context.Context, human OfficialHuman) (ResourceStatus, error) {
	object, err := control.apply(ctx, officialHumanGVR, "Human", human.Object)
	if err != nil {
		return ResourceStatus{}, err
	}
	return resourceStatus(object), nil
}

func (control *KubernetesControlPlane) GetTopology(ctx context.Context, teamName string) (RuntimeTopology, error) {
	if _, err := control.Discover(ctx); err != nil {
		return RuntimeTopology{}, err
	}
	teamName = strings.TrimSpace(teamName)
	if teamName == "" {
		return RuntimeTopology{}, fmt.Errorf("AgentTeams team name is required")
	}
	team, err := control.resource(officialTeamGVR).Get(ctx, teamName, metav1.GetOptions{})
	if err != nil {
		return RuntimeTopology{}, fmt.Errorf("get AgentTeams Team %q: %w", teamName, err)
	}
	mission, err := control.validateOwnedMissionObject(team, "Team")
	if err != nil {
		return RuntimeTopology{}, err
	}
	if err := validateMissionTeamShape(team, mission); err != nil {
		return RuntimeTopology{}, err
	}

	manager, err := control.resource(officialManagerGVR).Get(ctx, teamName+"-manager", metav1.GetOptions{})
	if err != nil {
		return RuntimeTopology{}, fmt.Errorf("get AgentTeams Manager for team %q: %w", teamName, err)
	}
	if err := control.validateSameMission(manager, mission, "Manager"); err != nil {
		return RuntimeTopology{}, err
	}

	human, err := control.resource(officialHumanGVR).Get(ctx, teamName+"-owner", metav1.GetOptions{})
	if err != nil {
		return RuntimeTopology{}, fmt.Errorf("get AgentTeams Human for team %q: %w", teamName, err)
	}
	if err := control.validateSameMission(human, mission, "Human"); err != nil {
		return RuntimeTopology{}, err
	}
	if err := validateHumanRuntimeIdentity(human, teamName+"-owner"); err != nil {
		return RuntimeTopology{}, err
	}

	functions := []model.AgentFunction{model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify}
	workers := make(map[model.AgentFunction]*unstructured.Unstructured, len(functions))
	for _, function := range functions {
		workerName := teamName + "-" + officialFunctionName(function)
		worker, workerErr := control.resource(officialWorkerGVR).Get(ctx, workerName, metav1.GetOptions{})
		if workerErr != nil {
			return RuntimeTopology{}, fmt.Errorf("get AgentTeams Worker %q: %w", workerName, workerErr)
		}
		if err := control.validateSameMission(worker, mission, "Worker"); err != nil {
			return RuntimeTopology{}, err
		}
		if worker.GetLabels()[HaoworkAgentFunctionLabel] != string(function) {
			return RuntimeTopology{}, fmt.Errorf("%w: Worker %q function label is not %q", ErrOfficialStatusPending, workerName, function)
		}
		if err := validateWorkerRuntimeIdentity(worker, workerName); err != nil {
			return RuntimeTopology{}, err
		}
		workers[function] = worker
	}

	teamRoomID, leaderRoomID, err := observedTeamStatus(team)
	if err != nil {
		return RuntimeTopology{}, err
	}
	managerPrincipal, err := observedPrincipal(manager, "Manager")
	if err != nil {
		return RuntimeTopology{}, err
	}
	managerRoomID, _, _ := unstructured.NestedString(manager.Object, "status", "roomID")
	humanPrincipal, err := observedPrincipal(human, "Human")
	if err != nil {
		return RuntimeTopology{}, err
	}
	principals := make(map[model.AgentFunction]string, len(functions))
	workerRooms := make(map[model.AgentFunction]string, len(functions))
	for _, function := range functions {
		worker := workers[function]
		principal, roomID, workerErr := observedWorkerPrincipal(worker)
		if workerErr != nil {
			return RuntimeTopology{}, workerErr
		}
		principals[function] = principal
		workerRooms[function] = roomID
	}
	if err := observedTeamMembers(team, principals, workerRooms); err != nil {
		return RuntimeTopology{}, err
	}

	return RuntimeTopology{
		MissionID:          mission.ID,
		TeamName:           teamName,
		ManagerPrincipalID: managerPrincipal,
		LeaderPrincipalID:  principals[model.FunctionDeliveryLeader],
		WorkerPrincipalIDs: map[model.AgentFunction]string{
			model.FunctionResearch: principals[model.FunctionResearch],
			model.FunctionBuild:    principals[model.FunctionBuild],
			model.FunctionVerify:   principals[model.FunctionVerify],
		},
		HumanPrincipalID: humanPrincipal,
		ManagerRoomID:    managerRoomID,
		LeaderRoomID:     leaderRoomID,
		TeamRoomID:       teamRoomID,
	}, nil
}

func validateMissionTeamShape(team *unstructured.Unstructured, mission missionIdentity) error {
	teamName := officialTeamName(mission.ID)
	if team.GetName() != teamName {
		return fmt.Errorf("%w: Team %q is not the deterministic name for Mission %q", ErrForeignControllerOwner, team.GetName(), mission.ID)
	}
	specTeamName, _, err := unstructured.NestedString(team.Object, "spec", "teamName")
	if err != nil || specTeamName != teamName {
		return fmt.Errorf("%w: Team %q has an invalid spec.teamName", ErrForeignControllerOwner, teamName)
	}
	if err := validateMissionTeamWorkerReferences(team, teamName); err != nil {
		return err
	}
	if err := validateMissionTeamHumanReferences(team, teamName); err != nil {
		return err
	}
	return nil
}

func validateMissionTeamWorkerReferences(team *unstructured.Unstructured, teamName string) error {
	members, found, err := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
	if err != nil || !found || len(members) != 4 {
		return fmt.Errorf("%w: Team %q must reference exactly four deterministic Workers", ErrForeignControllerOwner, teamName)
	}
	want := map[string]string{
		teamName + "-delivery-leader": "team_leader",
		teamName + "-research":        "worker",
		teamName + "-build":           "worker",
		teamName + "-verify":          "worker",
	}
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: Team %q has malformed Worker reference", ErrForeignControllerOwner, teamName)
		}
		name, nameOK := member["name"].(string)
		role, roleOK := member["role"].(string)
		if !nameOK || !roleOK || want[name] != role {
			return fmt.Errorf("%w: Team %q has an unexpected Worker reference", ErrForeignControllerOwner, teamName)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		return fmt.Errorf("%w: Team %q is missing deterministic Worker references", ErrForeignControllerOwner, teamName)
	}
	return nil
}

func validateMissionTeamHumanReferences(team *unstructured.Unstructured, teamName string) error {
	humanName := teamName + "-owner"
	admin, found, err := unstructured.NestedMap(team.Object, "spec", "admin")
	if err != nil || !found || !hasExactStringFields(admin, map[string]string{"name": humanName}) {
		return fmt.Errorf("%w: Team %q has an invalid Human admin reference", ErrForeignControllerOwner, teamName)
	}
	members, found, err := unstructured.NestedSlice(team.Object, "spec", "humanMembers")
	if err != nil || !found || len(members) != 1 {
		return fmt.Errorf("%w: Team %q must reference its deterministic Human owner", ErrForeignControllerOwner, teamName)
	}
	member, ok := members[0].(map[string]any)
	if !ok || !hasExactStringFields(member, map[string]string{"name": humanName, "role": "coordinator"}) {
		return fmt.Errorf("%w: Team %q has an invalid Human member reference", ErrForeignControllerOwner, teamName)
	}
	return nil
}

func hasExactStringFields(values map[string]any, want map[string]string) bool {
	if len(values) != len(want) {
		return false
	}
	for key, expected := range want {
		actual, ok := values[key].(string)
		if !ok || actual != expected {
			return false
		}
	}
	return true
}

func validateWorkerRuntimeIdentity(worker *unstructured.Unstructured, wantName string) error {
	workerName, found, err := unstructured.NestedString(worker.Object, "spec", "workerName")
	if err != nil || !found || workerName != wantName {
		return fmt.Errorf("%w: Worker %q has an invalid spec.workerName", ErrForeignControllerOwner, worker.GetName())
	}
	return nil
}

func validateHumanRuntimeIdentity(human *unstructured.Unstructured, wantName string) error {
	username, found, err := unstructured.NestedString(human.Object, "spec", "username")
	if err != nil || !found || username != wantName {
		return fmt.Errorf("%w: Human %q has an invalid spec.username", ErrForeignControllerOwner, human.GetName())
	}
	return nil
}

func validateMissionWorkerShape(worker *unstructured.Unstructured, mission missionIdentity) error {
	teamName := officialTeamName(mission.ID)
	function, ok := officialWorkerFunctionForName(teamName, worker.GetName())
	if !ok || worker.GetLabels()[HaoworkAgentFunctionLabel] != string(function) {
		return fmt.Errorf("%w: Worker %q is not a deterministic Mission Worker", ErrForeignControllerOwner, worker.GetName())
	}
	return validateWorkerRuntimeIdentity(worker, worker.GetName())
}

func validateMissionHumanShape(human *unstructured.Unstructured, mission missionIdentity) error {
	teamName := officialTeamName(mission.ID)
	wantName := teamName + "-owner"
	if human.GetName() != wantName {
		return fmt.Errorf("%w: Human %q is not the deterministic Mission owner", ErrForeignControllerOwner, human.GetName())
	}
	if err := validateHumanRuntimeIdentity(human, wantName); err != nil {
		return err
	}
	permission, found, err := unstructured.NestedInt64(human.Object, "spec", "permissionLevel")
	if err != nil || !found || permission != 2 {
		return fmt.Errorf("%w: Human %q has an invalid spec.permissionLevel", ErrForeignControllerOwner, wantName)
	}
	accessibleTeams, found, err := unstructured.NestedStringSlice(human.Object, "spec", "accessibleTeams")
	if err != nil || !found || len(accessibleTeams) != 1 || accessibleTeams[0] != teamName {
		return fmt.Errorf("%w: Human %q has invalid spec.accessibleTeams", ErrForeignControllerOwner, wantName)
	}
	return nil
}

func officialWorkerFunctionForName(teamName, workerName string) (model.AgentFunction, bool) {
	for _, function := range []model.AgentFunction{
		model.FunctionDeliveryLeader,
		model.FunctionResearch,
		model.FunctionBuild,
		model.FunctionVerify,
	} {
		if workerName == teamName+"-"+officialFunctionName(function) {
			return function, true
		}
	}
	return "", false
}

func (control *KubernetesControlPlane) StopMissionTeam(ctx context.Context, missionID string) error {
	if _, err := control.Discover(ctx); err != nil {
		return err
	}
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return fmt.Errorf("mission id is required")
	}
	selector := labels.Set{HaoworkMissionLabel: officialLabelValue(missionID)}.AsSelector().String()
	objects := make([]*unstructured.Unstructured, 0, 5)
	var mission missionIdentity
	for _, resource := range []struct {
		GVR  schema.GroupVersionResource
		Kind string
	}{
		{GVR: officialManagerGVR, Kind: "Manager"},
		{GVR: officialWorkerGVR, Kind: "Worker"},
	} {
		list, err := control.resource(resource.GVR).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return fmt.Errorf("list AgentTeams %s for mission %q: %w", resource.Kind, missionID, err)
		}
		for index := range list.Items {
			object := list.Items[index].DeepCopy()
			identity, err := control.validateOwnedMissionObject(object, resource.Kind)
			if err != nil {
				return err
			}
			if identity.ID != missionID {
				return fmt.Errorf("%w: %s %q belongs to mission %q, not %q", ErrForeignControllerOwner, resource.Kind, object.GetName(), identity.ID, missionID)
			}
			if mission.ID == "" {
				mission = identity
			}
			if identity != mission {
				return fmt.Errorf("%w: %s %q does not match mission hash for %q", ErrForeignControllerOwner, resource.Kind, object.GetName(), missionID)
			}
			objects = append(objects, object)
		}
	}
	if len(objects) != 5 {
		return fmt.Errorf("%w: mission %q has %d Manager/Worker resources, want 5", ErrOfficialStatusPending, missionID, len(objects))
	}
	if err := validateMissionRuntimeNames(objects, missionID); err != nil {
		return err
	}
	for _, object := range objects {
		if _, err := control.stopObject(ctx, resourceForKind(object.GetKind()), object); err != nil {
			return err
		}
	}
	return control.waitForStopped(ctx, objects)
}

func (control *KubernetesControlPlane) apply(ctx context.Context, resource schema.GroupVersionResource, kind string, desired *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	if _, err := control.Discover(ctx); err != nil {
		return nil, err
	}
	if err := control.validateDesiredObject(desired, kind); err != nil {
		return nil, err
	}
	client := control.resource(resource)
	current, err := client.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, createErr := client.Create(ctx, desired.DeepCopy(), metav1.CreateOptions{FieldManager: HaoworkAgentTeamsFieldManager})
		if createErr != nil {
			return nil, fmt.Errorf("create AgentTeams %s %q: %w", kind, desired.GetName(), createErr)
		}
		return created, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get AgentTeams %s %q before apply: %w", kind, desired.GetName(), err)
	}
	if _, err := control.validateOwnedMissionObject(current, kind); err != nil {
		return nil, err
	}
	if err := control.ensureSameMissionIdentity(current, desired); err != nil {
		return nil, err
	}
	updated := desired.DeepCopy()
	updated.SetResourceVersion(current.GetResourceVersion())
	updated.SetFinalizers(current.GetFinalizers())
	updated.SetOwnerReferences(current.GetOwnerReferences())
	updated.SetLabels(mergeManagedLabels(current.GetLabels(), desired.GetLabels()))
	updated.SetAnnotations(mergeManagedAnnotations(current.GetAnnotations(), desired.GetAnnotations()))
	if status, found, statusErr := unstructured.NestedFieldCopy(current.Object, "status"); statusErr != nil {
		return nil, fmt.Errorf("read current AgentTeams %s %q status: %w", kind, desired.GetName(), statusErr)
	} else if found {
		updated.Object["status"] = status
	}
	result, err := client.Update(ctx, updated, metav1.UpdateOptions{FieldManager: HaoworkAgentTeamsFieldManager})
	if err != nil {
		return nil, fmt.Errorf("update AgentTeams %s %q: %w", kind, desired.GetName(), err)
	}
	return result, nil
}

func (control *KubernetesControlPlane) stopObject(ctx context.Context, resource schema.GroupVersionResource, current *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	updated := current.DeepCopy()
	if err := unstructured.SetNestedField(updated.Object, "Stopped", "spec", "state"); err != nil {
		return nil, fmt.Errorf("set AgentTeams %s %q desired state: %w", current.GetKind(), current.GetName(), err)
	}
	result, err := control.resource(resource).Update(ctx, updated, metav1.UpdateOptions{FieldManager: HaoworkAgentTeamsFieldManager})
	if err != nil {
		return nil, fmt.Errorf("stop AgentTeams %s %q: %w", current.GetKind(), current.GetName(), err)
	}
	return result, nil
}

func (control *KubernetesControlPlane) waitForStopped(ctx context.Context, objects []*unstructured.Unstructured) error {
	timeout := control.StopObservationTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	interval := control.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		allStopped := true
		for _, object := range objects {
			observed, err := control.resource(resourceForKind(object.GetKind())).Get(ctx, object.GetName(), metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("observe stopped AgentTeams %s %q: %w", object.GetKind(), object.GetName(), err)
			}
			if !isObservedStopped(observed) {
				allStopped = false
			}
		}
		if allStopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("%w: AgentTeams resources did not observe Stopped state", ErrOfficialStatusPending)
		case <-ticker.C:
		}
	}
}

func (control *KubernetesControlPlane) validateCRD(ctx context.Context, definition officialResourceDefinition) error {
	crd, err := control.Dynamic.Resource(customResourceDefinitionGVR).Get(ctx, definition.Resource+"."+OfficialAPIGroup, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("%w: get %s.%s: %v", ErrOfficialCRDUnavailable, definition.Resource, OfficialAPIGroup, err)
	}
	group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
	kind, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "kind")
	plural, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
	if group != OfficialAPIGroup || kind != definition.Kind || plural != definition.Resource || !crdServesVersion(crd, OfficialAPIVersion) || !crdEstablished(crd) {
		return fmt.Errorf("%w: %s.%s does not match the pinned %s/%s %s CRD", ErrOfficialCRDUnavailable, definition.Resource, OfficialAPIGroup, OfficialAPIGroup, OfficialAPIVersion, definition.Kind)
	}
	return nil
}

func (control *KubernetesControlPlane) validateClient() error {
	if control == nil || control.Dynamic == nil || control.Discovery == nil || control.Namespace == "" || control.ControllerName == "" {
		return fmt.Errorf("Kubernetes dynamic client, discovery client, namespace, and controller name are required")
	}
	return nil
}

func (control *KubernetesControlPlane) resource(resource schema.GroupVersionResource) dynamic.ResourceInterface {
	return control.Dynamic.Resource(resource).Namespace(control.Namespace)
}

func (control *KubernetesControlPlane) validateDesiredObject(object *unstructured.Unstructured, wantKind string) error {
	if object == nil || object.GetAPIVersion() != OfficialAPIGroup+"/"+OfficialAPIVersion || object.GetKind() != wantKind || object.GetName() == "" || object.GetNamespace() != control.Namespace {
		return fmt.Errorf("invalid official AgentTeams %s resource", wantKind)
	}
	if _, hasStatus := object.Object["status"]; hasStatus {
		return fmt.Errorf("official AgentTeams %s request must not include status", wantKind)
	}
	mission, err := control.validateOwnedMissionObject(object, wantKind)
	if err != nil {
		return err
	}
	switch wantKind {
	case "Worker":
		return validateMissionWorkerShape(object, mission)
	case "Team":
		return validateMissionTeamShape(object, mission)
	case "Human":
		return validateMissionHumanShape(object, mission)
	default:
		return nil
	}
}

type missionIdentity struct {
	ID   string
	Hash string
}

func (control *KubernetesControlPlane) validateOwnedMissionObject(object *unstructured.Unstructured, kind string) (missionIdentity, error) {
	if object == nil {
		return missionIdentity{}, fmt.Errorf("%s resource is required", kind)
	}
	if object.GetLabels()[OfficialControllerOwnershipLabel] != control.ControllerName {
		return missionIdentity{}, fmt.Errorf("%w: %s %q", ErrForeignControllerOwner, kind, object.GetName())
	}
	missionID := strings.TrimSpace(object.GetAnnotations()[HaoworkMissionIDAnnotation])
	missionHash := strings.TrimSpace(object.GetAnnotations()[HaoworkMissionHashAnnotation])
	if missionID == "" || missionHash == "" || len(missionHash) != sha256HexLength || !isSHA256Hex(missionHash) || object.GetLabels()[HaoworkMissionLabel] != officialLabelValue(missionID) || object.GetLabels()[HaoworkEnvironmentLabel] == "" {
		return missionIdentity{}, fmt.Errorf("invalid Haowork mission ownership metadata on AgentTeams %s %q", kind, object.GetName())
	}
	return missionIdentity{ID: missionID, Hash: missionHash}, nil
}

const sha256HexLength = 64

func isSHA256Hex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func (control *KubernetesControlPlane) validateSameMission(object *unstructured.Unstructured, expected missionIdentity, kind string) error {
	actual, err := control.validateOwnedMissionObject(object, kind)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%w: %s %q does not match Mission %q and hash", ErrForeignControllerOwner, kind, object.GetName(), expected.ID)
	}
	return nil
}

func (control *KubernetesControlPlane) ensureSameMissionIdentity(current, desired *unstructured.Unstructured) error {
	for _, key := range []string{OfficialControllerOwnershipLabel, HaoworkMissionLabel, HaoworkEnvironmentLabel} {
		if current.GetLabels()[key] != desired.GetLabels()[key] {
			return fmt.Errorf("%w: resource %q %s label differs", ErrForeignControllerOwner, current.GetName(), key)
		}
	}
	for _, key := range []string{HaoworkMissionHashAnnotation, HaoworkMissionIDAnnotation} {
		if current.GetAnnotations()[key] != desired.GetAnnotations()[key] {
			return fmt.Errorf("%w: resource %q %s annotation differs", ErrForeignControllerOwner, current.GetName(), key)
		}
	}
	return nil
}

func resourceStatus(object *unstructured.Unstructured) ResourceStatus {
	if object == nil {
		return ResourceStatus{}
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	return ResourceStatus{Name: object.GetName(), Phase: phase}
}

func observedTeamStatus(team *unstructured.Unstructured) (string, string, error) {
	phase, _, _ := unstructured.NestedString(team.Object, "status", "phase")
	teamRoomID, _, _ := unstructured.NestedString(team.Object, "status", "teamRoomID")
	leaderRoomID, _, _ := unstructured.NestedString(team.Object, "status", "leaderDMRoomID")
	leaderReady, _, _ := unstructured.NestedBool(team.Object, "status", "leaderReady")
	readyWorkers, _, _ := unstructured.NestedInt64(team.Object, "status", "readyWorkers")
	totalWorkers, _, _ := unstructured.NestedInt64(team.Object, "status", "totalWorkers")
	if phase != "Active" || !leaderReady || readyWorkers != 3 || totalWorkers != 3 || teamRoomID == "" || leaderRoomID == "" {
		return "", "", fmt.Errorf("%w: Team %q requires Active phase, one ready leader, three ready workers, and rooms", ErrOfficialStatusPending, team.GetName())
	}
	return teamRoomID, leaderRoomID, nil
}

func observedPrincipal(object *unstructured.Unstructured, kind string) (string, error) {
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	principal, _, _ := unstructured.NestedString(object.Object, "status", "matrixUserID")
	if principal == "" || !isReadyPhase(kind, phase) {
		return "", fmt.Errorf("%w: %s %q has no ready Matrix principal", ErrOfficialStatusPending, kind, object.GetName())
	}
	if kind == "Manager" {
		if roomID, _, _ := unstructured.NestedString(object.Object, "status", "roomID"); roomID == "" {
			return "", fmt.Errorf("%w: Manager %q has no observed room", ErrOfficialStatusPending, object.GetName())
		}
	}
	return principal, nil
}

func observedWorkerPrincipal(worker *unstructured.Unstructured) (string, string, error) {
	principal, err := observedPrincipal(worker, "Worker")
	if err != nil {
		return "", "", err
	}
	roomID, _, _ := unstructured.NestedString(worker.Object, "status", "roomID")
	if roomID == "" {
		return "", "", fmt.Errorf("%w: Worker %q has no observed room", ErrOfficialStatusPending, worker.GetName())
	}
	return principal, roomID, nil
}

func observedTeamMembers(team *unstructured.Unstructured, principals map[model.AgentFunction]string, rooms map[model.AgentFunction]string) error {
	members, found, err := unstructured.NestedSlice(team.Object, "status", "members")
	if err != nil || !found || len(members) != 4 {
		return fmt.Errorf("%w: Team %q members are not fully observed", ErrOfficialStatusPending, team.GetName())
	}
	for _, function := range []model.AgentFunction{model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify} {
		wantName := team.GetName() + "-" + officialFunctionName(function)
		wantRole := "worker"
		if function == model.FunctionDeliveryLeader {
			wantRole = "team_leader"
		}
		foundMember := false
		for _, raw := range members {
			member, ok := raw.(map[string]any)
			if !ok || member["name"] != wantName {
				continue
			}
			foundMember = true
			if member["role"] != wantRole || member["matrixUserID"] != principals[function] || member["roomID"] != rooms[function] || member["observed"] != true || member["ready"] != true || member["phase"] != "Running" {
				return fmt.Errorf("%w: Team member %q status does not match Worker status", ErrOfficialStatusPending, wantName)
			}
		}
		if !foundMember {
			return fmt.Errorf("%w: Team %q omits member %q", ErrOfficialStatusPending, team.GetName(), wantName)
		}
	}
	return nil
}

func isReadyPhase(kind, phase string) bool {
	switch kind {
	case "Manager", "Worker":
		return phase == "Running"
	case "Human":
		return phase == "Active"
	default:
		return false
	}
}

func isObservedStopped(object *unstructured.Unstructured) bool {
	desiredState, _, _ := unstructured.NestedString(object.Object, "spec", "state")
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	containerState, _, _ := unstructured.NestedString(object.Object, "status", "containerState")
	return desiredState == "Stopped" || phase == "Stopped" || containerState == "Stopped"
}

func crdServesVersion(crd *unstructured.Unstructured, wantVersion string) bool {
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return false
	}
	for _, raw := range versions {
		version, ok := raw.(map[string]any)
		if ok && version["name"] == wantVersion && version["served"] == true && version["storage"] == true {
			return true
		}
	}
	return false
}

func mergeManagedAnnotations(current, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func mergeManagedLabels(current, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func crdEstablished(crd *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(crd.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if ok && condition["type"] == string(apiextensionsv1.Established) && condition["status"] == "True" {
			return true
		}
	}
	return false
}

type officialResourceDefinition struct {
	GVR      schema.GroupVersionResource
	Resource string
	Kind     string
}

func officialResourceDefinitions() []officialResourceDefinition {
	return []officialResourceDefinition{
		{GVR: officialManagerGVR, Resource: "managers", Kind: "Manager"},
		{GVR: officialWorkerGVR, Resource: "workers", Kind: "Worker"},
		{GVR: officialTeamGVR, Resource: "teams", Kind: "Team"},
		{GVR: officialHumanGVR, Resource: "humans", Kind: "Human"},
	}
}

func resourceForKind(kind string) schema.GroupVersionResource {
	for _, definition := range officialResourceDefinitions() {
		if definition.Kind == kind {
			return definition.GVR
		}
	}
	panic("unsupported AgentTeams resource kind " + kind)
}

func validateMissionRuntimeNames(objects []*unstructured.Unstructured, missionID string) error {
	teamName := officialTeamName(missionID)
	want := map[string]bool{
		"Manager/" + teamName + "-manager":        true,
		"Worker/" + teamName + "-delivery-leader": true,
		"Worker/" + teamName + "-research":        true,
		"Worker/" + teamName + "-build":           true,
		"Worker/" + teamName + "-verify":          true,
	}
	for _, object := range objects {
		key := object.GetKind() + "/" + object.GetName()
		if !want[key] {
			return fmt.Errorf("%w: mission %q contains unexpected runtime resource %s", ErrOfficialStatusPending, missionID, key)
		}
		if object.GetKind() == "Worker" {
			if err := validateWorkerRuntimeIdentity(object, object.GetName()); err != nil {
				return err
			}
		}
		delete(want, key)
	}
	if len(want) != 0 {
		return fmt.Errorf("%w: mission %q is missing expected runtime resources", ErrOfficialStatusPending, missionID)
	}
	return nil
}

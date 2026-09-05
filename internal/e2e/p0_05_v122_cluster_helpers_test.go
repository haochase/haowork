//go:build agentteams_cluster_e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	p005V122PublicNamespace   = "haowork-public"
	p005V122InternalNamespace = "haowork-internal"
	p005V122ClusterName       = "haowork-p005-v122"
)

const p005V122TopologyReadyTimeout = 8 * time.Minute
const p005V122FixtureTimeout = 15 * time.Minute

var (
	p005V122ManagerGVR = schema.GroupVersionResource{Group: agentteamsbridge.OfficialAPIGroup, Version: agentteamsbridge.OfficialAPIVersion, Resource: "managers"}
	p005V122WorkerGVR  = schema.GroupVersionResource{Group: agentteamsbridge.OfficialAPIGroup, Version: agentteamsbridge.OfficialAPIVersion, Resource: "workers"}
)

type p005V122ClusterFixture struct {
	t             *testing.T
	ctx           context.Context
	cancel        context.CancelFunc
	repoRoot      string
	contract      agentteamsbridge.OfficialContract
	restConfig    *rest.Config
	dynamic       dynamic.Interface
	discovery     discovery.DiscoveryInterface
	kubernetes    kubernetes.Interface
	evidence      *p005V122Evidence
	clusterName   string
	publicZone    p005V122Zone
	internalZone  p005V122Zone
	missionConfig p005V122MissionConfig
}

const p005V122CoreBridgeClientTimeout = 4 * time.Minute

type p005V122Zone struct {
	name      string
	namespace string
	release   string
}

type p005V122MissionConfig struct {
	missionID      string
	controllerName string
	model          string
	managerRuntime string
	managerImage   string
	workerRuntime  string
	mcpURL         string
	mcpServerName  string
	mcpTransport   string
	humanName      string
}

type p005V122Evidence struct {
	path string
}

type p005V122ProbeTarget struct {
	Component string
	URL       string
	Service   string
	Namespace string
}

func newP005V122ClusterFixture(t *testing.T) *p005V122ClusterFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), p005V122FixtureTimeout)
	t.Cleanup(cancel)
	repoRoot := p005V122FindRepoRoot(t)
	contract, err := agentteamsbridge.LoadOfficialContract(filepath.Join(repoRoot, "deploy", "agentteams", "v1.2.2", "upstream.lock.json"))
	if err != nil {
		t.Fatalf("BLOCKED_UPSTREAM_CONTRACT: %v", err)
	}
	if !contract.DeploymentReady() {
		t.Fatalf("BLOCKED_IMAGE_DIGEST: official v1.2.2 image inventory is not resolved")
	}
	kubeconfig := p005V122RequiredEnv(t, "KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("BLOCKED_KUBECONFIG: %v", err)
	}
	config.Timeout = 30 * time.Second
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatalf("BLOCKED_KUBERNETES_DYNAMIC_CLIENT: %v", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		t.Fatalf("BLOCKED_KUBERNETES_DISCOVERY_CLIENT: %v", err)
	}
	kubernetesClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("BLOCKED_KUBERNETES_CLIENT: %v", err)
	}
	evidencePath := p005V122EvidencePath(t, repoRoot)
	return &p005V122ClusterFixture{
		t: t, ctx: ctx, cancel: cancel, repoRoot: repoRoot, contract: contract, restConfig: config,
		dynamic: dynamicClient, discovery: discoveryClient, kubernetes: kubernetesClient,
		evidence: &p005V122Evidence{path: evidencePath}, clusterName: p005V122OptionalEnv("HAOWORK_P005_CLUSTER_NAME", p005V122ClusterName),
		publicZone:   p005V122Zone{name: "public", namespace: p005V122PublicNamespace, release: "haowork-public-agentteams"},
		internalZone: p005V122Zone{name: "internal", namespace: p005V122InternalNamespace, release: "haowork-internal-agentteams"},
	}
}

func (fixture *p005V122ClusterFixture) requireOfficialBaseline() {
	fixture.t.Helper()
	capabilities, err := agentteamsbridge.NewKubernetesControlPlane(fixture.dynamic, fixture.discovery, fixture.publicZone.namespace, "unused-for-readiness").Discover(fixture.ctx)
	if err != nil {
		fixture.t.Fatalf("BLOCKED_OFFICIAL_CRD_DISCOVERY: %v", err)
	}
	for _, kind := range []string{"Manager", "Worker", "Team", "Human"} {
		if !fixture.contract.HasKind(kind) || !capabilities.HasKind(kind) {
			fixture.t.Fatalf("BLOCKED_OFFICIAL_CRD_CONTRACT: required kind %s is unavailable", kind)
		}
	}
	for _, zone := range []p005V122Zone{fixture.publicZone, fixture.internalZone} {
		fixture.requireZoneReleaseIdentity(zone)
	}
	fixture.evidence.record(fixture.t, "baseline", map[string]any{
		"cluster_name": fixture.clusterName,
		"contract":     map[string]string{"tag": fixture.contract.Tag, "commit": fixture.contract.Commit, "chart_version": fixture.contract.ChartVersion},
		"crd_kinds":    []string{"Human", "Manager", "Team", "Worker"},
		"zones":        []map[string]string{{"name": fixture.publicZone.name, "namespace": fixture.publicZone.namespace, "release": fixture.publicZone.release}, {"name": fixture.internalZone.name, "namespace": fixture.internalZone.namespace, "release": fixture.internalZone.release}},
	})
}

func (fixture *p005V122ClusterFixture) requireZoneReleaseIdentity(zone p005V122Zone) {
	fixture.t.Helper()
	deploymentName := zone.release + "-controller"
	deployment, err := fixture.kubernetes.AppsV1().Deployments(zone.namespace).Get(fixture.ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_RELEASE_IDENTITY_%s: get controller %s/%s: %v", strings.ToUpper(zone.name), zone.namespace, deploymentName, err)
	}
	if deployment.Status.ObservedGeneration != deployment.Generation || deployment.Status.ReadyReplicas < 1 {
		fixture.t.Fatalf("BLOCKED_CONTROLLER_NOT_READY_%s: deployment %s generation=%d observed=%d ready=%d", strings.ToUpper(zone.name), deploymentName, deployment.Generation, deployment.Status.ObservedGeneration, deployment.Status.ReadyReplicas)
	}
	manager, err := fixture.dynamic.Resource(p005V122ManagerGVR).Namespace(zone.namespace).Get(fixture.ctx, "default", metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MANAGER_NOT_READY_%s: %v", strings.ToUpper(zone.name), err)
	}
	phase, _, err := unstructured.NestedString(manager.Object, "status", "phase")
	if err != nil || phase != "Running" {
		fixture.t.Fatalf("BLOCKED_MANAGER_NOT_READY_%s: status.phase=%q err=%v", strings.ToUpper(zone.name), phase, err)
	}
}

func (fixture *p005V122ClusterFixture) requireFiveRoleTopology() agentteamsbridge.RuntimeTopology {
	fixture.t.Helper()
	fixture.requireOfficialBaseline()
	config := fixture.missionConfigForPublicZone()
	mission := fixture.missionEnvelope(config.missionID)
	control := agentteamsbridge.NewKubernetesControlPlane(fixture.dynamic, fixture.discovery, fixture.publicZone.namespace, config.controllerName)
	orchestrator := agentteamsbridge.OfficialMissionOrchestrator{Control: control, Config: agentteamsbridge.OfficialResourceConfig{
		Namespace: fixture.publicZone.namespace, ControllerName: config.controllerName, Model: config.model,
		ManagerRuntime: config.managerRuntime, ManagerImage: config.managerImage, WorkerRuntime: config.workerRuntime, MCPServerName: config.mcpServerName,
		MCPServerURL: config.mcpURL, MCPTransport: config.mcpTransport, HumanName: config.humanName,
	}}
	var topology agentteamsbridge.RuntimeTopology
	var err error
	lastError := ""
	deadline := time.Now().Add(p005V122TopologyReadyTimeout)
	for time.Now().Before(deadline) {
		topology, err = orchestrator.EnsureMissionTeam(fixture.ctx, mission)
		if err == nil {
			break
		}
		if err.Error() != lastError {
			fixture.t.Logf("waiting for official topology: %v", err)
			lastError = err.Error()
		}
		select {
		case <-fixture.ctx.Done():
			fixture.t.Fatalf("BLOCKED_FIVE_ROLE_TOPOLOGY: %v", fixture.ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		fixture.t.Fatalf("BLOCKED_FIVE_ROLE_TOPOLOGY: %v", err)
	}
	if topology.ManagerPrincipalID == "" || topology.LeaderPrincipalID == "" || topology.HumanPrincipalID == "" || len(topology.WorkerPrincipalIDs) != 3 || topology.WorkerPrincipalIDs[model.FunctionResearch] == "" || topology.WorkerPrincipalIDs[model.FunctionBuild] == "" || topology.WorkerPrincipalIDs[model.FunctionVerify] == "" || topology.TeamRoomID == "" || topology.ManagerRoomID == "" || topology.LeaderRoomID == "" {
		fixture.t.Fatalf("BLOCKED_FIVE_ROLE_TOPOLOGY: observed topology is incomplete")
	}
	fixture.evidence.record(fixture.t, "topology", map[string]any{
		"team_name": topology.TeamName, "team_room_id": topology.TeamRoomID, "manager_room_id": topology.ManagerRoomID, "leader_room_id": topology.LeaderRoomID,
		"manager_principal_id": topology.ManagerPrincipalID, "leader_principal_id": topology.LeaderPrincipalID,
		"worker_principal_ids": topology.WorkerPrincipalIDs, "human_principal_id": topology.HumanPrincipalID,
	})
	return topology
}

func (fixture *p005V122ClusterFixture) requireRoleScopedSkills(topology agentteamsbridge.RuntimeTopology) {
	fixture.t.Helper()
	if topology.TeamName == "" {
		fixture.t.Fatal("BLOCKED_FIVE_ROLE_TOPOLOGY: team name is missing")
	}
	want := map[model.AgentFunction]string{model.FunctionResearch: "advisory", model.FunctionBuild: "patch", model.FunctionVerify: "audit"}
	observed := make(map[string][]string, len(want))
	for function, skill := range want {
		workerName := topology.TeamName + "-" + p005V122WorkerSuffix(function)
		worker, err := fixture.dynamic.Resource(p005V122WorkerGVR).Namespace(fixture.publicZone.namespace).Get(fixture.ctx, workerName, metav1.GetOptions{})
		if err != nil {
			fixture.t.Fatalf("BLOCKED_WORKER_SKILL_%s: %v", strings.ToUpper(string(function)), err)
		}
		skills, found, err := unstructured.NestedStringSlice(worker.Object, "spec", "skills")
		if err != nil || !found || !p005V122Contains(skills, skill) {
			fixture.t.Fatalf("BLOCKED_WORKER_SKILL_%s: required skill %q is absent", strings.ToUpper(string(function)), skill)
		}
		observed[string(function)] = skills
	}
	fixture.evidence.record(fixture.t, "skills", observed)
}

func (fixture *p005V122ClusterFixture) requireMatrixArtifactAndMCPDataPath(topology agentteamsbridge.RuntimeTopology) {
	fixture.t.Helper()
	fixture.requireRemoteCoreBridge()
	mission := fixture.missionEnvelope(fixture.missionConfigForPublicZone().missionID)
	executionID := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_EXECUTION_ID")
	runID, err := p005V122GovernedRunID("data", executionID)
	if err != nil {
		fixture.t.Fatalf("BLOCKED_CLUSTER_EXECUTION_ID: %v", err)
	}
	request := fixture.productionStartRequest(mission, topology, runID, "WORK-P005-V122-DATA-"+strings.TrimPrefix(runID, "RUN-P005-V122-DATA-"))
	evidence := fixture.startCoreBridge(mission, request)
	if len(evidence.SourceEventIDs) == 0 || len(evidence.Artifacts) == 0 || evidence.Cursor == "" {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_EVIDENCE: evidence=%#v", evidence)
	}
	artifact := evidence.Artifacts[0]
	artifactURI := "s3://" + fixture.managerArtifactBucket(fixture.publicZone) + "/" + artifact.Key
	parsedArtifact, err := url.Parse(artifactURI)
	if err != nil || parsedArtifact.Scheme != "s3" || parsedArtifact.Host == "" || strings.TrimPrefix(parsedArtifact.Path, "/") == "" || !p005V122SHA256(artifact.SHA256) || artifact.EnvironmentID != mission.EnvironmentID || artifact.Size <= 0 {
		fixture.t.Fatalf("BLOCKED_ARTIFACT_EVIDENCE: artifact=%#v err=%v", artifact, err)
	}
	fixture.configureMCPRuntimeBinding(topology, evidence.RuntimeBindingRevision)
	traceID := "TRC-P005-V122-" + strings.TrimPrefix(runID, "RUN-P005-V122-DATA-")
	fixture.callGovernedHistory(mission, request, traceID)
	evidence = fixture.readCoreBridgeEvidence(request.RunID)
	if !p005V122Contains(evidence.TraceIDs, traceID) || !p005V122SHA256(evidence.TraceSHA256) || !p005V122SHA256(evidence.CoreHistorySHA256) {
		fixture.t.Fatalf("BLOCKED_MCP_TRACE_EVIDENCE: evidence=%#v", evidence)
	}
	fixture.evidence.record(fixture.t, "data_path", map[string]any{
		"matrix":   map[string]any{"event_id": evidence.SourceEventIDs[0], "sender_id": topology.LeaderPrincipalID, "room_id": topology.LeaderRoomID, "mission_id": mission.ID},
		"artifact": map[string]any{"uri": artifactURI, "sha256": artifact.SHA256, "size": artifact.Size, "environment_id": artifact.EnvironmentID, "s3_key": strings.TrimPrefix(parsedArtifact.Path, "/")},
		"mcp":      map[string]any{"consumer_name": "manager", "route_name": "mcp-server-haowork-mcp.internal", "server_name": "haowork-mcp", "trace_id": traceID, "trace_sha256": evidence.TraceSHA256, "core_history_sha256": evidence.CoreHistorySHA256},
	})
}

func (fixture *p005V122ClusterFixture) managerArtifactBucket(zone p005V122Zone) string {
	fixture.t.Helper()
	pod, err := fixture.kubernetes.CoreV1().Pods(zone.namespace).Get(fixture.ctx, zone.release+"-manager", metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MANAGER_ARTIFACT_BUCKET: %v", err)
	}
	var buckets []string
	for _, container := range pod.Spec.Containers {
		for _, variable := range container.Env {
			if variable.Name == "AGENTTEAMS_FS_BUCKET" && variable.Value != "" {
				buckets = append(buckets, variable.Value)
			}
		}
	}
	if len(buckets) != 1 || !p005V122S3Bucket(buckets[0]) {
		fixture.t.Fatal("BLOCKED_MANAGER_ARTIFACT_BUCKET: official Manager does not expose one valid bucket")
	}
	return buckets[0]
}

func p005V122S3Bucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if character != '.' && character != '-' && (character < '0' || character > '9') && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func p005V122RequiredManagerImage(value string) string {
	value = strings.TrimSpace(value)
	if !regexp.MustCompile(`^[^\s@]+(?::[^\s@]+)?@sha256:[a-f0-9]{64}$`).MatchString(value) {
		return ""
	}
	return value
}

func (fixture *p005V122ClusterFixture) productionStartRequest(mission model.MissionEnvelope, topology agentteamsbridge.RuntimeTopology, runID, workItemID string) executor.AgentTeamsStartRequest {
	return executor.AgentTeamsStartRequest{
		RunID: runID, TaskID: "TASK-P005-V122", StepID: "STEP-P005-V122", MissionID: mission.ID, WorkItemID: workItemID,
		GoalVersion: mission.GoalVersion, ContextID: mission.ContextID, ContextHash: mission.ContextHash,
		LogicalActorID: mission.RoleAssignments[model.FunctionManager], RuntimePrincipalID: topology.ManagerPrincipalID,
		RuntimeBindingRevision: 1, AgentFunction: model.FunctionManager, EnvironmentID: mission.EnvironmentID, AgentTeamsInstanceID: topology.TeamName,
	}
}

func (fixture *p005V122ClusterFixture) startCoreBridge(mission model.MissionEnvelope, request executor.AgentTeamsStartRequest) corebridge.RunEvidence {
	fixture.t.Helper()
	var evidence corebridge.RunEvidence
	fixture.coreBridgeJSON(http.MethodPost, "/v1/runs/start", corebridge.StartRequest{Mission: mission, Request: request}, &evidence)
	return evidence
}

func (fixture *p005V122ClusterFixture) readCoreBridgeEvidence(runID string) corebridge.RunEvidence {
	fixture.t.Helper()
	var evidence corebridge.RunEvidence
	fixture.coreBridgeJSON(http.MethodGet, "/v1/runs/"+url.PathEscape(runID)+"/evidence", nil, &evidence)
	return evidence
}

func (fixture *p005V122ClusterFixture) coreBridgeJSON(method, path string, input, output any) {
	fixture.t.Helper()
	readyURL := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL")
	base, err := url.Parse(readyURL)
	if err != nil || base.Scheme != "http" || !isLoopbackForE2E(base.Hostname()) || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: ready URL must be loopback HTTP")
	}
	base.Path, base.RawPath, base.RawQuery, base.Fragment = path, "", "", ""
	var body io.Reader
	if input != nil {
		encoded, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			fixture.t.Fatal(marshalErr)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(fixture.ctx, method, base.String(), body)
	if err != nil {
		fixture.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN"))
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: p005V122CoreBridgeClientTimeout}).Do(request)
	if err != nil {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure map[string]any
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure)
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: status=%d error=%v reason=%v", response.StatusCode, failure["error"], failure["reason"])
	}
	if output != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(output); err != nil {
			fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_EVIDENCE: %v", err)
		}
	}
}

func (fixture *p005V122ClusterFixture) configureMCPRuntimeBinding(topology agentteamsbridge.RuntimeTopology, bindingRevision int) {
	fixture.t.Helper()
	if bindingRevision < 1 {
		fixture.t.Fatal("BLOCKED_MCP_RUNTIME_BINDING: Core Bridge did not return a persisted binding revision")
	}
	credentialSecret, err := fixture.kubernetes.CoreV1().Secrets(fixture.publicZone.namespace).Get(fixture.ctx, "agentteams-creds-default", metav1.GetOptions{})
	if err != nil || len(credentialSecret.Data["WORKER_GATEWAY_KEY"]) == 0 {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", err)
	}
	digest := sha256.Sum256(credentialSecret.Data["WORKER_GATEWAY_KEY"])
	document := map[string]any{"bindings": []any{map[string]any{
		"consumer_name": "manager", "credential_sha256": hex.EncodeToString(digest[:]),
		"principal": map[string]any{"logical_actor_id": "AGT-P005-MANAGER", "runtime_principal_id": topology.ManagerPrincipalID, "environment_id": "public", "agentteams_instance_id": topology.TeamName, "binding_revision": bindingRevision},
	}}}
	encoded, err := json.Marshal(document)
	if err != nil {
		fixture.t.Fatal(err)
	}
	secrets := fixture.kubernetes.CoreV1().Secrets(fixture.publicZone.namespace)
	secret, err := secrets.Get(fixture.ctx, "haowork-mcp-runtime-bindings", metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", err)
	}
	secret.Data = map[string][]byte{"bindings.json": encoded}
	updatedSecret, err := secrets.Update(fixture.ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", err)
	}
	deployments := fixture.kubernetes.AppsV1().Deployments(fixture.publicZone.namespace)
	deployment, err := deployments.Get(fixture.ctx, "haowork-mcp", metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", err)
	}
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	annotations, annotationErr := p005V122BindingRolloutAnnotations(encoded, updatedSecret.ResourceVersion)
	if annotationErr != nil {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", annotationErr)
	}
	for key, value := range annotations {
		deployment.Spec.Template.Annotations[key] = value
	}
	updated, err := deployments.Update(fixture.ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MCP_RUNTIME_BINDING: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		observed, getErr := deployments.Get(fixture.ctx, "haowork-mcp", metav1.GetOptions{})
		if getErr == nil && observed.Status.ObservedGeneration >= updated.Generation && observed.Status.Replicas == 1 && observed.Status.UpdatedReplicas == 1 && observed.Status.ReadyReplicas == 1 && observed.Status.AvailableReplicas == 1 && observed.Status.UnavailableReplicas == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	fixture.t.Fatal("BLOCKED_MCP_RUNTIME_BINDING: MCP rollout did not become ready")
}

func p005V122BindingRolloutAnnotations(document []byte, resourceVersion string) (map[string]string, error) {
	resourceVersion = strings.TrimSpace(resourceVersion)
	if len(document) == 0 || resourceVersion == "" {
		return nil, errors.New("runtime binding document and Secret resource version are required")
	}
	digest := sha256.Sum256(document)
	return map[string]string{
		"haowork.io/runtime-binding-sha256":           hex.EncodeToString(digest[:]),
		"haowork.io/runtime-binding-resource-version": resourceVersion,
	}, nil
}

func (fixture *p005V122ClusterFixture) callGovernedHistory(mission model.MissionEnvelope, run executor.AgentTeamsStartRequest, traceID string) {
	fixture.t.Helper()
	invocationID, err := p005V122GovernedInvocationID(p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_EXECUTION_ID"))
	if err != nil {
		fixture.t.Fatalf("BLOCKED_CLUSTER_EXECUTION_ID: %v", err)
	}
	secret, err := fixture.kubernetes.CoreV1().Secrets(fixture.publicZone.namespace).Get(fixture.ctx, "agentteams-creds-default", metav1.GetOptions{})
	if err != nil || len(secret.Data["WORKER_GATEWAY_KEY"]) == 0 {
		fixture.t.Fatalf("BLOCKED_MCP_GATEWAY_CREDENTIAL: %v", err)
	}
	gatewayURL := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MCP_GATEWAY_URL")
	parsed, err := url.Parse(gatewayURL)
	if err != nil || parsed.Scheme != "http" || !isLoopbackForE2E(parsed.Hostname()) || parsed.User != nil {
		fixture.t.Fatal("BLOCKED_MCP_GATEWAY_URL")
	}
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": "haowork.history", "arguments": map[string]any{"cursor": ""}, "meta": map[string]any{
			"id": invocationID, "mission_id": mission.ID, "task_id": run.TaskID, "work_item_id": run.TaskID, "run_id": run.RunID, "trace_id": traceID,
			"goal_version": mission.GoalVersion, "context_id": mission.ContextID, "context_hash": mission.ContextHash, "lease_id": mission.LeaseID, "scope": []string{"src/**"},
		},
	}}
	encoded, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(fixture.ctx, http.MethodPost, parsed.String(), bytes.NewReader(encoded))
	if err != nil {
		fixture.t.Fatal(err)
	}
	request.Host = "aigw-local.agentteams.io"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(secret.Data["WORKER_GATEWAY_KEY"]))
	response, err := (&http.Client{Timeout: 90 * time.Second}).Do(request)
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MCP_TOOLS_CALL: %v", err)
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		fixture.t.Fatalf("BLOCKED_MCP_TOOLS_CALL: read response: %v", readErr)
	}
	var result struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(responseBody, &result) != nil || len(result.Error) != 0 || len(result.Result) == 0 {
		fixture.t.Fatalf("BLOCKED_MCP_TOOLS_CALL: status=%d response=%s", response.StatusCode, string(responseBody))
	}
}

func isLoopbackForE2E(host string) bool {
	return host == "127.0.0.1" || strings.EqualFold(host, "localhost") || host == "::1"
}

func (fixture *p005V122ClusterFixture) requireCrossNamespaceTrafficDenied() {
	fixture.t.Helper()
	fixture.requireOfficialBaseline()
	for _, probe := range []p005V122Zone{fixture.publicZone, fixture.internalZone} {
		pods, err := fixture.kubernetes.CoreV1().Pods(probe.namespace).List(fixture.ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=haowork-network-probe,haowork.io/zone=" + probe.name})
		if err != nil {
			fixture.t.Fatalf("BLOCKED_NETWORK_PROBE_POD_%s: %v", strings.ToUpper(probe.name), err)
		}
		podName, err := p005V122UniqueRunningProbePod(pods.Items)
		if err != nil {
			fixture.t.Fatalf("BLOCKED_NETWORK_PROBE_POD_%s: %v", strings.ToUpper(probe.name), err)
		}
		targets, err := p005V122DefaultCrossZoneProbeTargets(probe.namespace)
		if err != nil {
			fixture.t.Fatalf("BLOCKED_NETWORK_PROBE_TARGETS_%s: %v", strings.ToUpper(probe.name), err)
		}
		for _, target := range targets {
			fixture.requireProbeTargetService(target)
			policyVerified := fixture.requireCrossZoneEgressDenyPolicy(probe, target)
			if err := fixture.expectProbeDenied(probe.namespace, podName, target, policyVerified); err != nil {
				fixture.t.Fatalf("BLOCKED_CROSS_NAMESPACE_DENIAL_%s: component=%s err=%v", strings.ToUpper(probe.name), target.Component, err)
			}
		}
	}
	fixture.evidence.record(fixture.t, "network_policy", map[string]any{"public_to_internal": "denied", "internal_to_public": "denied"})
}

func p005V122UniqueRunningProbePod(pods []corev1.Pod) (string, error) {
	running := make([]string, 0, 1)
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning && len(pod.Spec.Containers) == 1 && pod.Spec.Containers[0].Name == "probe" {
			running = append(running, pod.Name)
		}
	}
	if len(running) != 1 {
		return "", fmt.Errorf("expected one running probe Pod, got %d", len(running))
	}
	return running[0], nil
}

func (fixture *p005V122ClusterFixture) requireCrossZoneEgressDenyPolicy(source p005V122Zone, target p005V122ProbeTarget) bool {
	fixture.t.Helper()
	policyName := "haowork-" + source.name + "-default-deny"
	policy, err := fixture.kubernetes.NetworkingV1().NetworkPolicies(source.namespace).Get(fixture.ctx, policyName, metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_NETWORK_POLICY_EVIDENCE: get %s/%s: %v", source.namespace, policyName, err)
	}
	if !p005V122PolicyDeniesOppositeNamespace(*policy, source.name, target.Namespace) {
		fixture.t.Fatalf("BLOCKED_NETWORK_POLICY_EVIDENCE: %s/%s does not prove egress denial to %s", source.namespace, policyName, target.Namespace)
	}
	return true
}

func (fixture *p005V122ClusterFixture) requireProbeTargetService(target p005V122ProbeTarget) {
	fixture.t.Helper()
	if _, err := fixture.kubernetes.CoreV1().Services(target.Namespace).Get(fixture.ctx, target.Service, metav1.GetOptions{}); err != nil {
		fixture.t.Fatalf("BLOCKED_NETWORK_PROBE_SERVICE: component=%s err=%v", target.Component, err)
	}
	endpoints, err := fixture.kubernetes.CoreV1().Endpoints(target.Namespace).Get(fixture.ctx, target.Service, metav1.GetOptions{})
	if err != nil || len(endpoints.Subsets) == 0 {
		fixture.t.Fatalf("BLOCKED_NETWORK_PROBE_ENDPOINTS: component=%s err=%v", target.Component, err)
	}
}

func (fixture *p005V122ClusterFixture) requireRestartResumeWithoutDuplicateGovernanceEvents(topology agentteamsbridge.RuntimeTopology) {
	fixture.t.Helper()
	fixture.requireRemoteCoreBridge()
	mission := fixture.missionEnvelope(fixture.missionConfigForPublicZone().missionID)
	executionID := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_EXECUTION_ID")
	runID, err := p005V122GovernedRunID("restart", executionID)
	if err != nil {
		fixture.t.Fatalf("BLOCKED_CLUSTER_EXECUTION_ID: %v", err)
	}
	firstRequest := fixture.productionStartRequest(mission, topology, runID, "WORK-P005-V122-RESUME-ONE-"+strings.TrimPrefix(runID, "RUN-P005-V122-RESTART-"))
	before := fixture.startCoreBridge(mission, firstRequest)
	if before.Cursor == "" || len(before.SourceEventIDs) == 0 {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: first evidence=%#v", before)
	}
	beforeIDs := make(map[string]struct{}, len(before.SourceEventIDs))
	for _, sourceID := range before.SourceEventIDs {
		if sourceID == "" {
			fixture.t.Fatal("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: first run has an empty Matrix event ID")
		}
		beforeIDs[sourceID] = struct{}{}
	}
	if len(beforeIDs) != len(before.SourceEventIDs) {
		fixture.t.Fatal("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: first run duplicated a Matrix event ID")
	}
	fixture.restartCoreBridge()
	fixture.requireRemoteCoreBridge()
	secondRequest := fixture.productionStartRequest(mission, topology, firstRequest.RunID, "WORK-P005-V122-RESUME-TWO-"+strings.TrimPrefix(runID, "RUN-P005-V122-RESTART-"))
	after := fixture.startCoreBridge(mission, secondRequest)
	if after.Cursor == "" || len(after.SourceEventIDs) <= len(beforeIDs) || after.Cursor == before.Cursor {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: resumed evidence=%#v", after)
	}
	newEventID := ""
	seen := make(map[string]int, len(after.SourceEventIDs))
	for _, sourceID := range after.SourceEventIDs {
		seen[sourceID]++
	}
	for sourceID := range beforeIDs {
		if seen[sourceID] != 1 {
			fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: source event %q count=%d after resume", sourceID, seen[sourceID])
		}
	}
	for sourceID, count := range seen {
		if count != 1 {
			fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: source event %q count=%d after resume", sourceID, count)
		}
		if _, existed := beforeIDs[sourceID]; !existed {
			newEventID = sourceID
		}
	}
	if newEventID == "" {
		fixture.t.Fatal("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: resumed run emitted no new Matrix event")
	}
	fixture.evidence.record(fixture.t, "restart", map[string]any{
		"opaque_cursor_before": before.Cursor,
		"opaque_cursor_after":  after.Cursor,
		"new_event_id":         newEventID,
	})
}

func (fixture *p005V122ClusterFixture) restartCoreBridge() {
	fixture.t.Helper()
	deployments := fixture.kubernetes.AppsV1().Deployments(fixture.publicZone.namespace)
	deployment, err := deployments.Get(fixture.ctx, "haowork-core-bridge", metav1.GetOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: %v", err)
	}
	if deployment.Spec.Template.Annotations == nil {
		deployment.Spec.Template.Annotations = map[string]string{}
	}
	deployment.Spec.Template.Annotations["haowork.io/e2e-restarted-at"] = time.Now().UTC().Format(time.RFC3339Nano)
	updated, err := deployments.Update(fixture.ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		observed, getErr := deployments.Get(fixture.ctx, "haowork-core-bridge", metav1.GetOptions{})
		if getErr == nil && observed.Generation >= updated.Generation && observed.Status.ObservedGeneration >= updated.Generation && observed.Status.Replicas == 1 && observed.Status.UpdatedReplicas == 1 && observed.Status.ReadyReplicas == 1 && observed.Status.AvailableReplicas == 1 && observed.Status.UnavailableReplicas == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	fixture.t.Fatal("BLOCKED_HAOWORK_CORE_BRIDGE_RESTART: rollout did not become ready")
}

func (fixture *p005V122ClusterFixture) missionConfigForPublicZone() p005V122MissionConfig {
	if fixture.missionConfig.missionID != "" {
		return fixture.missionConfig
	}
	fixture.missionConfig = p005V122MissionConfig{
		missionID:      p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MISSION_ID"),
		controllerName: p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_CONTROLLER_NAME"),
		model:          p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MODEL"),
		managerRuntime: p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MANAGER_RUNTIME"),
		managerImage:   p005V122RequiredManagerImage(p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MANAGER_IMAGE")),
		workerRuntime:  p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_WORKER_RUNTIME"),
		mcpURL:         p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MCP_URL"),
		mcpServerName:  p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MCP_SERVER_NAME"),
		mcpTransport:   p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_MCP_TRANSPORT"),
		humanName:      p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_HUMAN_NAME"),
	}
	if fixture.missionConfig.managerImage == "" {
		fixture.t.Fatal("BLOCKED_CLUSTER_MANAGER_IMAGE")
	}
	return fixture.missionConfig
}

func (fixture *p005V122ClusterFixture) missionEnvelope(missionID string) model.MissionEnvelope {
	fixture.t.Helper()
	issuedAt := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: missionID, ProjectID: "PRJ-P005-V122", ContextID: "CTX-P005-V122", ContextHash: "cluster-e2e-context", LeaseID: "LSE-P005-V122", PolicyVersion: "P005-V122", GoalVersion: 1,
		GovernanceTaskIDs: []string{"TASK-P005-V122"}, CompletionCriteria: []string{"verified cluster evidence"}, AllowedScopes: []string{"src/**"},
		AllowedSkills:   []model.MissionSkillGrant{{Name: "history", Version: "1.0.0"}, {Name: "advisory", Version: "1.0.0"}, {Name: "patch", Version: "1.0.0"}, {Name: "audit", Version: "1.0.0"}},
		RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-P005-MANAGER", model.FunctionDeliveryLeader: "AGT-P005-LEADER", model.FunctionResearch: "AGT-P005-RESEARCH", model.FunctionBuild: "AGT-P005-BUILD", model.FunctionVerify: "AGT-P005-VERIFY"},
		RiskLevel:       "L0", EnvironmentID: "public", IssuedAt: issuedAt, Deadline: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		fixture.t.Fatalf("BLOCKED_MISSION_ENVELOPE: %v", err)
	}
	return mission
}

func (fixture *p005V122ClusterFixture) requireRemoteCoreBridge() {
	fixture.t.Helper()
	const deploymentName = "haowork-core-bridge"
	deployment, err := fixture.kubernetes.AppsV1().Deployments(fixture.publicZone.namespace).Get(fixture.ctx, deploymentName, metav1.GetOptions{})
	if err != nil || deployment.Status.ObservedGeneration != deployment.Generation || deployment.Status.ReadyReplicas < 1 {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: deployment %s/%s is not ready: %v", fixture.publicZone.namespace, deploymentName, err)
	}
	endpoints, err := fixture.kubernetes.CoreV1().Endpoints(fixture.publicZone.namespace).Get(fixture.ctx, deploymentName, metav1.GetOptions{})
	if err != nil || len(endpoints.Subsets) == 0 {
		fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: service endpoints are unavailable: %v", err)
	}
	readyURL := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_CORE_BRIDGE_READY_URL")
	token := p005V122RequiredEnv(fixture.t, "HAOWORK_P005_CLUSTER_CORE_BRIDGE_TOKEN")
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(fixture.ctx, http.MethodGet, readyURL, nil)
		if err != nil {
			fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: ready request: %v", err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err == nil {
			var readiness struct {
				MissionResolverReady     bool `json:"mission_resolver_ready"`
				RuntimeBindingStoreReady bool `json:"runtime_binding_store_ready"`
				TraceStoreReady          bool `json:"trace_store_ready"`
				ProductionTransportReady bool `json:"production_transport_ready"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&readiness)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && readiness.MissionResolverReady && readiness.RuntimeBindingStoreReady && readiness.TraceStoreReady && readiness.ProductionTransportReady {
				return
			}
			lastErr = fmt.Errorf("status=%d readiness=%+v decode=%v", response.StatusCode, readiness, decodeErr)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	fixture.t.Fatalf("BLOCKED_HAOWORK_CORE_BRIDGE: required mission/runtime-binding/trace/transport readiness is unavailable: %v", lastErr)
}

func (fixture *p005V122ClusterFixture) expectProbeDenied(namespace, podName string, target p005V122ProbeTarget, policyVerified bool) error {
	pod, err := fixture.kubernetes.CoreV1().Pods(namespace).Get(fixture.ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pod.Status.Phase != corev1.PodRunning || len(pod.Spec.Containers) != 1 {
		return fmt.Errorf("probe pod is not a single-container running Pod")
	}
	request := fixture.kubernetes.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(podName).SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{Container: pod.Spec.Containers[0].Name, Command: []string{"/haowork-network-probe", "probe", target.URL}, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(fixture.restConfig, http.MethodPost, request.URL())
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(fixture.ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	return p005V122ClassifyProbeResult(err, stderr.String(), policyVerified)
}

func (evidence *p005V122Evidence) record(t *testing.T, section string, value any) {
	t.Helper()
	current := map[string]any{"schema_version": 1}
	if content, err := os.ReadFile(evidence.path); err == nil {
		if err := json.Unmarshal(content, &current); err != nil {
			t.Fatalf("BLOCKED_EVIDENCE_READ: %v", err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("BLOCKED_EVIDENCE_READ: %v", err)
	}
	current[section] = value
	if err := p005V122RejectSensitiveEvidence(current); err != nil {
		t.Fatalf("BLOCKED_EVIDENCE_SENSITIVE_DATA: %v", err)
	}
	encoded, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		t.Fatalf("BLOCKED_EVIDENCE_ENCODE: %v", err)
	}
	temporary := evidence.path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("BLOCKED_EVIDENCE_WRITE: %v", err)
	}
	if err := os.Rename(temporary, evidence.path); err != nil {
		t.Fatalf("BLOCKED_EVIDENCE_WRITE: %v", err)
	}
}

var p005V122SensitiveEvidenceKey = regexp.MustCompile(`(?i)(token|password|secret|authorization|credential)`)

func p005V122RejectSensitiveEvidence(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return fmt.Errorf("normalize evidence: %w", err)
	}
	return p005V122RejectSensitiveEvidenceValue(normalized, "")
}

func p005V122RejectSensitiveEvidenceValue(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if p005V122SensitiveEvidenceKey.MatchString(key) {
				return fmt.Errorf("sensitive key %q", childPath)
			}
			if err := p005V122RejectSensitiveEvidenceValue(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := p005V122RejectSensitiveEvidenceValue(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(strings.ToLower(typed), "bearer ") {
			return fmt.Errorf("authorization-like value at %q", path)
		}
	}
	return nil
}

func p005V122FindRepoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("BLOCKED_WORKTREE: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("BLOCKED_WORKTREE: go.mod was not found")
		}
		directory = parent
	}
}

func p005V122EvidencePath(t *testing.T, repoRoot string) string {
	t.Helper()
	directory := p005V122RequiredEnv(t, "HAOWORK_P005_CLUSTER_EVIDENCE_DIR")
	wantRoot := filepath.Clean(filepath.Join(repoRoot, ".haowork", "cache", "evidence", "p0-05-v1.2.2", "cluster"))
	path := filepath.Clean(directory)
	if path != wantRoot {
		t.Fatal("BLOCKED_EVIDENCE_DIR: evidence directory must be the project-local cluster evidence path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("BLOCKED_EVIDENCE_DIR: %v", err)
	}
	return filepath.Join(path, "cluster-evidence.json")
}

func p005V122RequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("BLOCKED_CLUSTER_CONFIGURATION: %s is required", name)
	}
	return value
}

func p005V122OptionalEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func p005V122DelimitedEnv(t *testing.T, name string) []string {
	t.Helper()
	values := strings.Split(p005V122RequiredEnv(t, name), ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func p005V122DefaultCrossZoneProbeTargets(sourceNamespace string) ([]p005V122ProbeTarget, error) {
	oppositeNamespace := p005V122InternalNamespace
	if sourceNamespace == p005V122InternalNamespace {
		oppositeNamespace = p005V122PublicNamespace
	} else if sourceNamespace != p005V122PublicNamespace {
		return nil, errors.New("source namespace is not a P0-05 zone")
	}
	zoneName := strings.TrimPrefix(oppositeNamespace, "haowork-")
	if zoneName != "public" && zoneName != "internal" {
		return nil, errors.New("opposite namespace is not a P0-05 zone")
	}
	servicePrefix := "haowork-" + zoneName + "-agentteams-"
	targets := []p005V122ProbeTarget{
		{Component: "matrix", Service: servicePrefix + "tuwunel", Namespace: oppositeNamespace, URL: "http://" + servicePrefix + "tuwunel." + oppositeNamespace + ".svc.cluster.local:6167/_matrix/client/versions"},
		{Component: "minio", Service: servicePrefix + "minio", Namespace: oppositeNamespace, URL: "http://" + servicePrefix + "minio." + oppositeNamespace + ".svc.cluster.local:9000/minio/health/live"},
		{Component: "higress", Service: "higress-gateway", Namespace: oppositeNamespace, URL: "http://higress-gateway." + oppositeNamespace + ".svc.cluster.local/"},
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left].Component < targets[right].Component })
	return targets, nil
}

func p005V122GovernedRunID(kind, executionID string) (string, error) {
	normalizedKind := strings.ToUpper(strings.TrimSpace(kind))
	normalizedExecutionID := strings.ToUpper(strings.TrimSpace(executionID))
	if (normalizedKind != "DATA" && normalizedKind != "RESTART") || !regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{2,47}$`).MatchString(normalizedExecutionID) {
		return "", errors.New("execution ID must be a 3-48 character alphanumeric-hyphen identifier")
	}
	return "RUN-P005-V122-" + normalizedKind + "-" + normalizedExecutionID, nil
}

func p005V122GovernedInvocationID(executionID string) (string, error) {
	normalizedExecutionID := strings.ToUpper(strings.TrimSpace(executionID))
	if !regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{2,47}$`).MatchString(normalizedExecutionID) {
		return "", errors.New("execution ID must be a 3-48 character alphanumeric-hyphen identifier")
	}
	return "INV-P005-V122-" + normalizedExecutionID, nil
}

func p005V122ClassifyProbeResult(runErr error, stderr string, policyVerified bool) error {
	if runErr == nil {
		return errors.New("cross-namespace request unexpectedly succeeded")
	}
	value := strings.ToLower(runErr.Error() + "\n" + stderr)
	for _, invalid := range []string{"not found", "no such file", "x509", "certificate", "bad address", "no such host", "invalid url"} {
		if strings.Contains(value, invalid) {
			return fmt.Errorf("probe runtime/configuration failure: %s", invalid)
		}
	}
	for _, denied := range []string{"timed out", "timeout", "deadline exceeded", "egress denied", "networkpolicy denied", "network policy denied", "administratively prohibited"} {
		if strings.Contains(value, denied) {
			return nil
		}
	}
	if policyVerified {
		for _, ambiguous := range []string{"exit code 4", "connection refused", "network is unreachable"} {
			if strings.Contains(value, ambiguous) {
				return nil
			}
		}
	}
	return fmt.Errorf("probe failed for a non-isolation reason: %s", runErr)
}

func p005V122PolicyDeniesOppositeNamespace(policy networkingv1.NetworkPolicy, sourceZone, targetNamespace string) bool {
	if policy.Spec.PodSelector.Size() != 0 || !p005V122ContainsNetworkPolicyType(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress) {
		return false
	}
	targetZone := strings.TrimPrefix(targetNamespace, "haowork-")
	if targetZone == "" || targetZone == sourceZone {
		return false
	}
	for _, rule := range policy.Spec.Egress {
		if len(rule.To) == 0 {
			return false
		}
		for _, peer := range rule.To {
			if peer.IPBlock != nil || peer.NamespaceSelector == nil {
				continue
			}
			if peer.NamespaceSelector.MatchLabels["haowork.io/zone"] == targetZone {
				return false
			}
			for _, expression := range peer.NamespaceSelector.MatchExpressions {
				if expression.Key == "haowork.io/zone" && (expression.Operator != metav1.LabelSelectorOpNotIn || !p005V122Contains(expression.Values, targetZone)) {
					return false
				}
			}
		}
	}
	return true
}

func p005V122ContainsNetworkPolicyType(values []networkingv1.PolicyType, want networkingv1.PolicyType) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func p005V122Contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func p005V122WorkerSuffix(function model.AgentFunction) string {
	if function == model.FunctionDeliveryLeader {
		return "delivery-leader"
	}
	return string(function)
}

func p005V122SHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

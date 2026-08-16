//go:build agentteams_e2e

package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

// TestP005RealAgentTeamsCrossZoneDelivery is intentionally an explicit real-
// infrastructure test. It must never silently fall back to a fake transport.
func TestP005RealAgentTeamsCrossZoneDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env, err := loadP005Environment(ctx)
	if err != nil {
		t.Fatalf("P0-05 real AgentTeams preflight failed: %v", err)
	}
	publicControl := agentteamsbridge.NewPinnedControlClient(env.PublicControl, p005HTTPClient(env.PublicToken), env.PublicIdentity)
	internalControl := agentteamsbridge.NewPinnedControlClient(env.InternalControl, p005HTTPClient(env.InternalToken), env.InternalIdentity)
	for name, control := range map[string]*agentteamsbridge.ControlClient{"public": publicControl, "internal": internalControl} {
		profile, detectErr := control.Detect(ctx)
		if detectErr != nil {
			t.Fatalf("%s AgentTeams capability detection: %v", name, detectErr)
		}
		if !profile.IsStable() {
			t.Fatalf("%s AgentTeams profile = %#v, want stable %s/%s", name, profile, agentteamsbridge.StableVersion, agentteamsbridge.StableAPIVersion)
		}
	}
	internalMatrix := agentteamsbridge.NewHTTPMatrixClient(env.InternalMatrix, p005HTTPClient(env.InternalToken), env.InternalIdentity)
	if _, err := internalMatrix.Sync(ctx, ""); err != nil {
		t.Fatalf("internal Matrix pull failed: %v", err)
	}
	internalArtifacts := agentteamsbridge.NewHTTPArtifactStore(env.InternalArtifacts, p005HTTPClient(env.InternalToken), env.InternalIdentity)
	healthArtifact := []byte("haowork-p005-internal-artifact\n")
	healthDigest := sha256.Sum256(healthArtifact)
	healthRef, err := internalArtifacts.Upload(ctx, "e2e/p005-health.txt", healthArtifact, hex.EncodeToString(healthDigest[:]))
	if err != nil {
		t.Fatalf("internal Artifact upload failed: %v", err)
	}
	downloaded, err := agentteamsbridge.VerifyArtifactDownload(ctx, internalArtifacts, healthRef, hex.EncodeToString(healthDigest[:]))
	if err != nil || len(downloaded) != len(healthArtifact) {
		t.Fatalf("internal Artifact pull verification failed: bytes=%d err=%v", len(downloaded), err)
	}
	badMatrix := agentteamsbridge.NewHTTPMatrixClient("http://127.0.0.1:1", p005HTTPClient(env.InternalToken), env.InternalIdentity)
	if _, err := badMatrix.Sync(ctx, ""); err == nil {
		t.Fatal("invalid internal Matrix endpoint unexpectedly succeeded")
	}
	badArtifacts := agentteamsbridge.NewHTTPArtifactStore("http://127.0.0.1:1", p005HTTPClient(env.InternalToken), env.InternalIdentity)
	if _, err := badArtifacts.Download(ctx, "e2e/p005-health.txt"); err == nil {
		t.Fatal("invalid internal Artifact endpoint unexpectedly succeeded")
	}

	root := newGitCapsule(t)
	project := openProject(t, ctx, root)
	owner := model.Actor{ID: "USR-P005-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	seedP005Agents(t, ctx, root, owner)
	mission := p005Mission()
	orchestrator := agentteamsbridge.MissionOrchestrator{Control: publicControl}
	topology, err := orchestrator.EnsureMissionTeam(ctx, mission)
	if err != nil {
		t.Fatalf("create real Manager/Leader/Worker topology: %v", err)
	}
	if topology.ManagerPrincipalID == "" || len(topology.WorkerPrincipalIDs) != 3 || topology.HumanPrincipalID == "" {
		t.Fatalf("incomplete real topology: %#v", topology)
	}
	internalTopology, err := (agentteamsbridge.MissionOrchestrator{Control: internalControl}).EnsureMissionTeam(ctx, mission)
	if err != nil {
		t.Fatalf("create isolated internal AgentTeams topology: %v", err)
	}
	if internalTopology.ManagerPrincipalID == topology.ManagerPrincipalID || internalTopology.TeamRoomID == topology.TeamRoomID {
		t.Fatalf("public and internal AgentTeams identities/rooms are not isolated: public=%#v internal=%#v", topology, internalTopology)
	}

	transport := agentteamsbridge.NewLegacyTransportForMigrationTest(agentteamsbridge.TransportConfig{
		Orchestrator: orchestrator,
		Mission: func(id string) (model.MissionEnvelope, error) {
			if id != mission.ID {
				return model.MissionEnvelope{}, fmt.Errorf("unknown mission %q", id)
			}
			return mission, nil
		},
		Trace:           trace.New(root),
		RuntimeBindings: serviceBindingStore{service: project.Service},
		BindingActor:    owner,
	}, env.PublicMatrix, env.PublicArtifacts, p005HTTPClient(env.PublicToken), env.PublicIdentity)
	session, err := transport.Start(ctx, executorStartRequest(mission, topology))
	if err != nil {
		t.Fatalf("start real AgentTeams governed run: %v", err)
	}
	events := make([]executor.AgentTeamsEvent, 0)
	roles := make(map[string]bool)
	artifactCount := 0
	for event := range session.Events(ctx, "") {
		if event.SourceEventID == "" || event.AdapterCursor == "" || event.WorkspaceDigest == "" {
			t.Fatalf("event missing source/cursor/workspace checkpoint: %#v", event)
		}
		events = append(events, event)
		roles[event.ActorRole] = true
		for _, artifact := range event.Artifacts {
			if len(artifact.SHA256) != 64 || artifact.URI == "" {
				t.Fatalf("Matrix artifact lacks verified URI/SHA-256: %#v", artifact)
			}
			artifactCount++
		}
	}
	if len(events) == 0 {
		t.Fatal("real AgentTeams produced no Matrix events")
	}
	for _, role := range []string{"manager", "delivery_leader", "build", "verify"} {
		if !roles[role] {
			t.Fatalf("real AgentTeams did not produce a %s event; roles=%#v", role, roles)
		}
	}
	if artifactCount == 0 {
		t.Fatal("real AgentTeams produced no verified artifact with SHA-256/size/environment")
	}
	reconnect, err := transport.Start(ctx, executorStartRequest(mission, topology))
	if err != nil {
		t.Fatalf("reconnect real AgentTeams run: %v", err)
	}
	for event := range reconnect.Events(ctx, events[len(events)-1].AdapterCursor) {
		if event.SourceEventID == events[len(events)-1].SourceEventID {
			t.Fatalf("reconnect replayed checkpointed Matrix source event %q", event.SourceEventID)
		}
	}
	if err := reconnect.Cancel(ctx); err != nil {
		t.Fatalf("stop reconnected AgentTeams topology: %v", err)
	}
	if err := session.Cancel(ctx); err != nil {
		t.Fatalf("stop real AgentTeams topology: %v", err)
	}
	state, err := project.Service.Status(ctx)
	if err != nil {
		t.Fatalf("read Core state after topology bind: %v", err)
	}
	if len(state.RuntimeBindings) != 5 {
		t.Fatalf("runtime bindings = %d, want Manager/Leader/Research/Build/Verify", len(state.RuntimeBindings))
	}

	resources, err := agentteamsbridge.RenderResources(mission)
	if err != nil {
		t.Fatalf("render role-scoped resources: %v", err)
	}
	if len(resources) != 3 || !bytes.Contains(resources[1].Spec, []byte("research-worker")) || !bytes.Contains(resources[1].Spec, []byte("verify-worker")) {
		t.Fatalf("AgentTeams resource topology omits required roles: %#v", resources)
	}

	assertP005BrowserCookieBoundary(t, ctx, root, project)
	var stdout, stderr bytes.Buffer
	if code := cli.Execute(ctx, []string{"status", "--project", root, "--json"}, &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("CLI status exit=%d stderr=%q", code, stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("CLI status is not JSON: %q", stdout.String())
	}
	if _, err := trace.New(root).ReadAll(ctx); err != nil {
		t.Fatalf("trace replay after real run: %v", err)
	}
}

type p005Environment struct {
	PublicControl, InternalControl, PublicMatrix, InternalMatrix, PublicArtifacts, InternalArtifacts string
	PublicIdentity, InternalIdentity, PublicToken, InternalToken                                     string
}

func loadP005Environment(context.Context) (p005Environment, error) {
	values := p005Environment{
		PublicControl: os.Getenv("HAOWORK_P005_PUBLIC_CONTROL_URL"), InternalControl: os.Getenv("HAOWORK_P005_INTERNAL_CONTROL_URL"),
		PublicMatrix: os.Getenv("HAOWORK_P005_PUBLIC_MATRIX_URL"), InternalMatrix: os.Getenv("HAOWORK_P005_INTERNAL_MATRIX_URL"),
		PublicArtifacts: os.Getenv("HAOWORK_P005_PUBLIC_ARTIFACT_URL"), InternalArtifacts: os.Getenv("HAOWORK_P005_INTERNAL_ARTIFACT_URL"),
		PublicIdentity: os.Getenv("HAOWORK_P005_PUBLIC_IDENTITY"), InternalIdentity: os.Getenv("HAOWORK_P005_INTERNAL_IDENTITY"),
		PublicToken: os.Getenv("HAOWORK_P005_PUBLIC_TOKEN"), InternalToken: os.Getenv("HAOWORK_P005_INTERNAL_TOKEN"),
	}
	for name, value := range map[string]string{
		"HAOWORK_P005_PUBLIC_CONTROL_URL": values.PublicControl, "HAOWORK_P005_INTERNAL_CONTROL_URL": values.InternalControl,
		"HAOWORK_P005_PUBLIC_MATRIX_URL": values.PublicMatrix, "HAOWORK_P005_INTERNAL_MATRIX_URL": values.InternalMatrix,
		"HAOWORK_P005_PUBLIC_ARTIFACT_URL": values.PublicArtifacts, "HAOWORK_P005_INTERNAL_ARTIFACT_URL": values.InternalArtifacts,
		"HAOWORK_P005_PUBLIC_IDENTITY": values.PublicIdentity, "HAOWORK_P005_INTERNAL_IDENTITY": values.InternalIdentity,
		"HAOWORK_P005_PUBLIC_TOKEN": values.PublicToken, "HAOWORK_P005_INTERNAL_TOKEN": values.InternalToken,
	} {
		if strings.TrimSpace(value) == "" {
			return p005Environment{}, fmt.Errorf("%s is required; real dual-environment deployment is not configured", name)
		}
	}
	return values, nil
}

type p005AuthTransport struct {
	base  http.RoundTripper
	token string
}

func (transport p005AuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(copy)
}
func p005HTTPClient(token string) *http.Client {
	base := http.DefaultTransport
	return &http.Client{Transport: p005AuthTransport{base: base, token: token}, Timeout: 30 * time.Second}
}

type serviceBindingStore struct{ service *app.Service }

func (store serviceBindingStore) BindRuntimeTopology(ctx context.Context, bindings []model.RuntimeBinding, actor model.Actor) ([]model.RuntimeBinding, error) {
	if store.service == nil {
		return nil, errors.New("Core service is required")
	}
	return store.service.BindRuntimeTopology(ctx, bindings, actor)
}

func p005Mission() model.MissionEnvelope {
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{ID: "MSN-P005-REAL", ProjectID: "PRJ-WORKBENCH", GoalVersion: 1, ContextID: "CTX-P005", ContextHash: "context-p005", EnvironmentID: "public", GovernanceTaskIDs: []string{"TASK-P005"}, CompletionCriteria: []string{"independent verify evidence"}, AllowedScopes: []string{"src/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "advisory", Version: "1.0.0"}, {Name: "mirror", Version: "1.0.0"}, {Name: "patch", Version: "1.0.0"}, {Name: "audit", Version: "1.0.0"}}, RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-P005-MANAGER", model.FunctionDeliveryLeader: "AGT-P005-LEADER", model.FunctionResearch: "AGT-P005-RESEARCH", model.FunctionBuild: "AGT-P005-BUILD", model.FunctionVerify: "AGT-P005-VERIFY"}, RiskLevel: "L2", IssuedAt: time.Unix(1, 0).UTC(), Deadline: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		panic(err)
	}
	return mission
}

func seedP005Agents(t *testing.T, ctx context.Context, root string, owner model.Actor) {
	t.Helper()
	functions := []model.AgentFunction{model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify}
	store := eventstore.New(root)
	history, err := store.ReadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]model.Event, 0, len(functions))
	for index, function := range functions {
		id := "AGT-P005-" + strings.ToUpper(string(function))
		payload, marshalErr := json.Marshal(model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: id, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: function}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		events = append(events, model.Event{ID: fmt.Sprintf("EVT-P005-AGENT-%02d", index), Type: "agent.identity.registered", ProjectID: "PRJ-WORKBENCH", GoalVersion: 1, AggregateType: "agent", AggregateID: id, Actor: owner, OccurredAt: time.Now().UTC(), Payload: payload})
	}
	if _, err := store.AppendBatchIfUnchanged(ctx, events, len(history)); err != nil {
		t.Fatalf("seed logical agents: %v", err)
	}
}

func executorStartRequest(mission model.MissionEnvelope, topology agentteamsbridge.RuntimeTopology) executor.AgentTeamsStartRequest {
	return executor.AgentTeamsStartRequest{RunID: "RUN-P005-REAL", TaskID: "TASK-P005", StepID: "STEP-P005-REAL", MissionID: mission.ID, WorkItemID: "WI-P005-REAL", GoalVersion: mission.GoalVersion, ContextID: mission.ContextID, ContextHash: mission.ContextHash, LogicalActorID: mission.RoleAssignments[model.FunctionManager], RuntimePrincipalID: topology.ManagerPrincipalID, RuntimeBindingRevision: 1, AgentFunction: model.FunctionManager, EnvironmentID: mission.EnvironmentID, AgentTeamsInstanceID: topology.TeamName}
}

func assertP005BrowserCookieBoundary(t *testing.T, ctx context.Context, root string, project core.Project) {
	t.Helper()
	server := &localapi.Server{Project: project}
	manager := localcore.NewManager(nil)
	serveContext, stop := context.WithCancel(ctx)
	defer stop()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- manager.ServeWithHandler(serveContext, root, func(metadata localcore.Metadata, stopCore func()) http.Handler {
			server.Metadata, server.ControlKey, server.Stop = metadata, metadata.ControlKey, stopCore
			return server.Handler()
		})
	}()
	metadata := waitForCore(t, ctx, root)
	client := newLoopbackHTTPClient(t)
	unauthenticated, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.Endpoint+"/api/v1/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(unauthenticated)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Workbench request without browser cookie status=%d, want 401", response.StatusCode)
	}
	control := newControlAPIClient(metadata, client)
	bootstrap, err := control.CreateBrowserSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	browser := &fakeBrowser{}
	if err := localcore.OpenBrowser(ctx, browser, metadata.Endpoint, bootstrap); err != nil {
		t.Fatal(err)
	}
	exchangeBrowserSession(t, ctx, client, browser.target, bootstrap)
	authenticated, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.Endpoint+"/api/v1/project", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(authenticated)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Workbench request with browser cookie status=%d, want 200", response.StatusCode)
	}
	server.Stop()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

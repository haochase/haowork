package agentteamsbridge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
	"k8s.io/apimachinery/pkg/runtime"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestProductionBridgeUsesOfficialKubernetesMatrixS3AndHigress(t *testing.T) {
	transport, err := agentteamsbridge.NewProductionTransport(productionConfig(t))
	if err != nil {
		t.Fatalf("NewProductionTransport() error = %v", err)
	}
	if transport == nil {
		t.Fatal("NewProductionTransport() returned nil transport")
	}
}

func TestProductionBridgeRejectsMissingOfficialDependency(t *testing.T) {
	config := productionConfig(t)
	config.Higress = nil
	if _, err := agentteamsbridge.NewProductionTransport(config); err == nil {
		t.Fatal("NewProductionTransport() accepted missing Higress inspector")
	}

	config = productionConfig(t)
	config.Kubernetes = nil
	if _, err := agentteamsbridge.NewProductionTransport(config); err == nil {
		t.Fatal("NewProductionTransport() accepted missing Kubernetes client")
	}
}

func TestProductionBridgeRejectsLegacyProfile(t *testing.T) {
	profile := agentteamsbridge.CapabilityProfile{
		Name: "hi-claw/agent-teams", Version: "v1.1.2", APIVersion: "hiclaw.io/v1beta1",
		ResourceKinds: map[string]bool{"Manager": true, "Team": true, "Worker": true, "Human": true},
		Controller:    true, Matrix: true, MinIO: true, HigressMCP: true,
	}
	if profile.IsStable() {
		t.Fatal("legacy capability profile is accepted as production stable")
	}
}

func TestProductionBridgeChecksHigressBeforeDelegation(t *testing.T) {
	want := errors.New("Higress route is unavailable")
	inspector := &productionHigress{err: want}
	config := productionConfig(t)
	config.Higress = inspector
	transport, err := agentteamsbridge.NewProductionTransport(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.Start(context.Background(), executor.AgentTeamsStartRequest{EnvironmentID: "ENV-001"})
	if !errors.Is(err, want) {
		t.Fatalf("Start() error = %v, want Higress readiness error", err)
	}
	if inspector.expectation.MCPServerName != "haowork-mcp" || inspector.expectation.ConsumerName != "haowork-agentteams" || inspector.expectation.RouteName != "haowork-mcp-route" {
		t.Fatalf("Higress expectation = %#v", inspector.expectation)
	}
}

func TestProductionBridgeRejectsWrongEnvironmentBeforeAnyRemoteCall(t *testing.T) {
	matrix := &productionMatrix{}
	artifacts := &productionArtifacts{}
	inspector := &productionHigress{}
	config := productionConfig(t)
	config.Matrix = matrix
	config.Artifacts = artifacts
	config.Higress = inspector
	transport, err := agentteamsbridge.NewProductionTransport(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Start(context.Background(), executor.AgentTeamsStartRequest{EnvironmentID: "ENV-INTERNAL"}); err == nil {
		t.Fatal("Start() accepted an environment different from the production transport")
	}
	if inspector.calls != 0 || matrix.calls != 0 || artifacts.calls != 0 {
		t.Fatalf("wrong environment contacted remote dependencies: higress=%d matrix=%d artifacts=%d", inspector.calls, matrix.calls, artifacts.calls)
	}
}

func productionConfig(t *testing.T) agentteamsbridge.ProductionConfig {
	t.Helper()
	return agentteamsbridge.ProductionConfig{
		EnvironmentID:   "ENV-001",
		Namespace:       "agentteams-public",
		ControllerName:  "agentteams-controller",
		Kubernetes:      dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		Discovery:       &discoveryfake.FakeDiscovery{},
		Matrix:          &productionMatrix{},
		Artifacts:       &productionArtifacts{},
		Higress:         &productionHigress{},
		MCPServerName:   "haowork-mcp",
		MCPConsumerName: "haowork-agentteams",
		MCPRouteName:    "haowork-mcp-route",
		ResourceConfig: agentteamsbridge.OfficialResourceConfig{
			Model: "model", ManagerRuntime: "manager", ManagerImage: "registry.example.test/agentteams-manager:v1.2.2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkerRuntime: "worker", HumanName: "Owner",
			MCPServerURL: "http://haowork-mcp.agentteams-public.svc.cluster.local:8080/mcp", MCPTransport: "http",
		},
		RuntimeBindings: productionBindings{},
		Trace:           trace.New(t.TempDir()),
		Mission: func(string) (model.MissionEnvelope, error) {
			return model.MissionEnvelope{}, errors.New("not called by constructor")
		},
		BindingActor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}
}

type productionMatrix struct{ calls int }

func (matrix *productionMatrix) Send(context.Context, string, agentteamsbridge.MatrixOutbound) error {
	matrix.calls++
	return nil
}
func (matrix *productionMatrix) Sync(context.Context, string) (agentteamsbridge.MatrixPage, error) {
	matrix.calls++
	return agentteamsbridge.MatrixPage{}, nil
}

type productionArtifacts struct{ calls int }

func (artifacts *productionArtifacts) Upload(context.Context, string, []byte, string) (string, error) {
	artifacts.calls++
	return "artifact", nil
}
func (artifacts *productionArtifacts) Download(context.Context, string) ([]byte, error) {
	artifacts.calls++
	return nil, nil
}

type productionHigress struct {
	expectation agentteamsbridge.HigressExpectation
	err         error
	calls       int
}

func (higress *productionHigress) Inspect(_ context.Context, expectation agentteamsbridge.HigressExpectation) (agentteamsbridge.HigressInspection, error) {
	higress.calls++
	higress.expectation = expectation
	return agentteamsbridge.HigressInspection{}, higress.err
}

type productionBindings struct{}

func (productionBindings) BindRuntimeTopology(context.Context, []model.RuntimeBinding, model.Actor) ([]model.RuntimeBinding, error) {
	return nil, nil
}

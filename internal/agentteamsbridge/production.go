package agentteamsbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
)

// HigressVerifier is the small, credential-safe production boundary used to
// prove the governed MCP route before any AgentTeams delegation starts.
type HigressVerifier interface {
	Inspect(context.Context, HigressExpectation) (HigressInspection, error)
}

// ProductionConfig is the complete official AgentTeams v1.2.2 dependency
// set. No retired REST control, Matrix wrapper, or artifact URL is accepted.
type ProductionConfig struct {
	EnvironmentID    string
	Namespace        string
	ControllerName   string
	Kubernetes       dynamic.Interface
	Discovery        discovery.DiscoveryInterface
	Matrix           MatrixClient
	Artifacts        ArtifactStore
	Higress          HigressVerifier
	MCPServerName    string
	MCPConsumerName  string
	MCPRouteName     string
	ResourceConfig   OfficialResourceConfig
	RuntimeBindings  RuntimeBindingStore
	Trace            trace.Store
	Mission          func(string) (model.MissionEnvelope, error)
	BindingActor     model.Actor
	MaxArtifactBytes int64
}

// NewProductionTransport creates the sole production bridge path. The
// Kubernetes dynamic client, Matrix v3 client, S3-compatible artifact store,
// and Higress MCP verification are all required and failures are terminal.
func NewProductionTransport(config ProductionConfig) (*Transport, error) {
	if err := validateProductionConfig(config); err != nil {
		return nil, err
	}
	resources, err := normalizedProductionResources(config)
	if err != nil {
		return nil, err
	}
	control := NewKubernetesControlPlane(config.Kubernetes, config.Discovery, config.Namespace, config.ControllerName)
	return NewTransport(TransportConfig{
		Orchestrator: OfficialMissionOrchestrator{Control: control, Config: resources},
		Matrix:       config.Matrix, Artifacts: config.Artifacts, MaxArtifactBytes: config.MaxArtifactBytes,
		Mission: config.Mission, Trace: config.Trace, RuntimeBindings: config.RuntimeBindings, BindingActor: config.BindingActor,
		EmptyMatrixPollLimit: 10, EmptyMatrixPollInterval: time.Second,
		ExpectedEnvironmentID: strings.TrimSpace(config.EnvironmentID),
		Ready: func(ctx context.Context) error {
			_, err := config.Higress.Inspect(ctx, HigressExpectation{
				ConsumerName: config.MCPConsumerName, RouteName: config.MCPRouteName, MCPServerName: config.MCPServerName,
			})
			return err
		},
	}), nil
}

func validateProductionConfig(config ProductionConfig) error {
	if strings.TrimSpace(config.EnvironmentID) == "" || strings.TrimSpace(config.Namespace) == "" || strings.TrimSpace(config.ControllerName) == "" {
		return errors.New("official AgentTeams environment, namespace, and controller are required")
	}
	if config.Kubernetes == nil || config.Discovery == nil || config.Matrix == nil || config.Artifacts == nil || config.Higress == nil {
		return errors.New("official Kubernetes, Matrix v3, S3 artifact, and Higress dependencies are required")
	}
	if strings.TrimSpace(config.MCPServerName) == "" || strings.TrimSpace(config.MCPConsumerName) == "" || strings.TrimSpace(config.MCPRouteName) == "" {
		return errors.New("official Higress MCP server, consumer, and route are required")
	}
	if config.RuntimeBindings == nil || config.Trace == nil || config.Mission == nil {
		return errors.New("governed runtime bindings, trace store, and mission resolver are required")
	}
	if strings.TrimSpace(config.BindingActor.ID) == "" || config.BindingActor.Kind != model.ActorHuman || config.BindingActor.Role != model.RoleOwner {
		return errors.New("human owner binding actor is required")
	}
	return nil
}

func normalizedProductionResources(config ProductionConfig) (OfficialResourceConfig, error) {
	resources := config.ResourceConfig
	if resources.Namespace != "" && strings.TrimSpace(resources.Namespace) != strings.TrimSpace(config.Namespace) {
		return OfficialResourceConfig{}, fmt.Errorf("official resource namespace does not match production namespace")
	}
	if resources.ControllerName != "" && strings.TrimSpace(resources.ControllerName) != strings.TrimSpace(config.ControllerName) {
		return OfficialResourceConfig{}, fmt.Errorf("official resource controller does not match production controller")
	}
	if resources.MCPServerName != "" && strings.TrimSpace(resources.MCPServerName) != strings.TrimSpace(config.MCPServerName) {
		return OfficialResourceConfig{}, fmt.Errorf("official resource MCP server does not match production MCP server")
	}
	resources.Namespace = strings.TrimSpace(config.Namespace)
	resources.ControllerName = strings.TrimSpace(config.ControllerName)
	resources.MCPServerName = strings.TrimSpace(config.MCPServerName)
	if strings.TrimSpace(resources.Model) == "" || strings.TrimSpace(resources.ManagerRuntime) == "" || strings.TrimSpace(resources.WorkerRuntime) == "" || strings.TrimSpace(resources.HumanName) == "" || strings.TrimSpace(resources.MCPServerURL) == "" || strings.TrimSpace(resources.MCPTransport) == "" {
		return OfficialResourceConfig{}, errors.New("official AgentTeams resource model, runtimes, human, and MCP endpoint are required")
	}
	return resources, nil
}

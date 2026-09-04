package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/model"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type runtimeConfig struct {
	listenAddr, stateRoot, token, projectID                string
	environmentID, namespace, controllerName               string
	matrixURL                                              string
	s3Endpoint, s3AccessKey, s3SecretKey, s3Bucket         string
	higressURL, higressUsername, higressPassword           string
	mcpServerName, mcpConsumerName, mcpRouteName           string
	modelName, managerRuntime, managerImage, workerRuntime string
	mcpURL, mcpTransport, humanName                        string
}

func requiredEnvironmentNames() []string {
	return []string{
		"HAOWORK_CORE_BRIDGE_LISTEN_ADDR", "HAOWORK_CORE_BRIDGE_STATE_ROOT", "HAOWORK_CORE_BRIDGE_TOKEN", "HAOWORK_CORE_PROJECT_ID",
		"HAOWORK_CORE_ENVIRONMENT_ID", "HAOWORK_CORE_NAMESPACE", "HAOWORK_CORE_CONTROLLER_NAME",
		"HAOWORK_CORE_MATRIX_URL", "HAOWORK_CORE_S3_ENDPOINT",
		"HAOWORK_CORE_S3_ACCESS_KEY", "HAOWORK_CORE_S3_SECRET_KEY", "HAOWORK_CORE_S3_BUCKET",
		"HAOWORK_CORE_HIGRESS_CONSOLE_URL", "HAOWORK_CORE_HIGRESS_USERNAME", "HAOWORK_CORE_HIGRESS_PASSWORD",
		"HAOWORK_CORE_MCP_SERVER_NAME", "HAOWORK_CORE_MCP_CONSUMER_NAME", "HAOWORK_CORE_MCP_ROUTE_NAME",
		"HAOWORK_CORE_MODEL", "HAOWORK_CORE_MANAGER_RUNTIME", "HAOWORK_CORE_MANAGER_IMAGE", "HAOWORK_CORE_WORKER_RUNTIME",
		"HAOWORK_CORE_MCP_URL", "HAOWORK_CORE_MCP_TRANSPORT", "HAOWORK_CORE_HUMAN_NAME",
	}
}

func loadConfig() (runtimeConfig, error) {
	values := make(map[string]string)
	for _, name := range requiredEnvironmentNames() {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			return runtimeConfig{}, fmt.Errorf("required Core Bridge environment %s is unavailable", name)
		}
		values[name] = value
	}
	return runtimeConfig{
		listenAddr: values["HAOWORK_CORE_BRIDGE_LISTEN_ADDR"], stateRoot: values["HAOWORK_CORE_BRIDGE_STATE_ROOT"], token: values["HAOWORK_CORE_BRIDGE_TOKEN"], projectID: values["HAOWORK_CORE_PROJECT_ID"],
		environmentID: values["HAOWORK_CORE_ENVIRONMENT_ID"], namespace: values["HAOWORK_CORE_NAMESPACE"], controllerName: values["HAOWORK_CORE_CONTROLLER_NAME"],
		matrixURL:  values["HAOWORK_CORE_MATRIX_URL"],
		s3Endpoint: values["HAOWORK_CORE_S3_ENDPOINT"], s3AccessKey: values["HAOWORK_CORE_S3_ACCESS_KEY"], s3SecretKey: values["HAOWORK_CORE_S3_SECRET_KEY"], s3Bucket: values["HAOWORK_CORE_S3_BUCKET"],
		higressURL: values["HAOWORK_CORE_HIGRESS_CONSOLE_URL"], higressUsername: values["HAOWORK_CORE_HIGRESS_USERNAME"], higressPassword: values["HAOWORK_CORE_HIGRESS_PASSWORD"],
		mcpServerName: values["HAOWORK_CORE_MCP_SERVER_NAME"], mcpConsumerName: values["HAOWORK_CORE_MCP_CONSUMER_NAME"], mcpRouteName: values["HAOWORK_CORE_MCP_ROUTE_NAME"],
		modelName: values["HAOWORK_CORE_MODEL"], managerRuntime: values["HAOWORK_CORE_MANAGER_RUNTIME"], managerImage: values["HAOWORK_CORE_MANAGER_IMAGE"], workerRuntime: values["HAOWORK_CORE_WORKER_RUNTIME"],
		mcpURL: values["HAOWORK_CORE_MCP_URL"], mcpTransport: values["HAOWORK_CORE_MCP_TRANSPORT"], humanName: values["HAOWORK_CORE_HUMAN_NAME"],
	}, nil
}

func (config runtimeConfig) redacted() map[string]string {
	return map[string]string{"environment_id": config.environmentID, "namespace": config.namespace, "controller_name": config.controllerName}
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	state, err := corebridge.OpenState(config.stateRoot)
	if err != nil {
		log.Fatal(err)
	}
	if err := state.InitializeProject(context.Background(), config.projectID); err != nil {
		log.Fatal(err)
	}
	kubernetesConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal(err)
	}
	dynamicClient, err := dynamic.NewForConfig(kubernetesConfig)
	if err != nil {
		log.Fatal(err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubernetesConfig)
	if err != nil {
		log.Fatal(err)
	}

	factory := func(mission model.MissionEnvelope) (corebridge.Starter, error) {
		if mission.EnvironmentID != config.environmentID {
			return nil, errors.New("Mission environment does not match Core Bridge zone")
		}
		matrixClient, err := agentteamsbridge.NewKubernetesHumanMatrixClient(dynamicClient, config.namespace, agentteamsbridge.MatrixV3Config{
			BaseURL: config.matrixURL, AllowInsecureClusterLocal: true,
		})
		if err != nil {
			return nil, err
		}
		artifacts, err := agentteamsbridge.NewS3ArtifactStore(agentteamsbridge.S3ArtifactStoreConfig{
			Endpoint: config.s3Endpoint, AccessKey: config.s3AccessKey, SecretKey: config.s3SecretKey,
			Bucket: config.s3Bucket, EnvironmentID: config.environmentID, MissionID: mission.ID, MaxBytes: 16 << 20,
		})
		if err != nil {
			return nil, err
		}
		higress, err := agentteamsbridge.NewHigressInspector(agentteamsbridge.HigressConfig{
			ConsoleURL: config.higressURL, Username: config.higressUsername, Password: config.higressPassword, AllowInsecureClusterLocal: true,
		})
		if err != nil {
			return nil, err
		}
		return agentteamsbridge.NewProductionTransport(agentteamsbridge.ProductionConfig{
			EnvironmentID: config.environmentID, Namespace: config.namespace, ControllerName: config.controllerName,
			Kubernetes: dynamicClient, Discovery: discoveryClient, Matrix: matrixClient, Artifacts: artifacts, Higress: higress,
			MCPServerName: config.mcpServerName, MCPConsumerName: config.mcpConsumerName, MCPRouteName: config.mcpRouteName,
			ResourceConfig: agentteamsbridge.OfficialResourceConfig{
				Namespace: config.namespace, ControllerName: config.controllerName, Model: config.modelName,
				ManagerRuntime: config.managerRuntime, ManagerImage: config.managerImage, WorkerRuntime: config.workerRuntime, MCPServerName: config.mcpServerName,
				MCPServerURL: config.mcpURL, MCPTransport: config.mcpTransport, HumanName: config.humanName,
			},
			RuntimeBindings: state, Trace: state.TraceStore(), Mission: state.Mission,
			BindingActor: model.Actor{ID: "USR-CORE-BRIDGE-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}, MaxArtifactBytes: 16 << 20,
		})
	}
	server, err := corebridge.NewServer(corebridge.Config{
		Token: config.token, State: state, Factory: factory, RunTimeout: 90 * time.Second,
		ReportError: func(stage string, err error) { log.Printf("Core Bridge %s failed: %v", stage, err) },
	})
	if err != nil {
		log.Fatal(err)
	}
	httpServer := &http.Server{Addr: config.listenAddr, Handler: server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 30 * time.Second}
	log.Printf("haowork-core-bridge listening on %s for namespace %s", config.listenAddr, config.namespace)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

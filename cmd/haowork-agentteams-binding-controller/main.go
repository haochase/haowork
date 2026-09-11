package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbinding"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	envNamespace      = "HAOWORK_BINDING_NAMESPACE"
	envControllerName = "HAOWORK_BINDING_CONTROLLER_NAME"
	envGovernanceRoot = "HAOWORK_BINDING_GOVERNANCE_ROOT"
	envBindingRoot    = "HAOWORK_BINDING_OUTPUT_ROOT"
	envAllowedSecrets = "HAOWORK_BINDING_ALLOWED_SECRET_NAMES"
	envBindingTTL     = "HAOWORK_BINDING_TTL"
)

type config struct {
	namespace, controllerName, governanceRoot, bindingRoot string
	allowedSecretNames                                     []string
	bindingTTL                                             time.Duration
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "haowork-agentteams-binding-controller:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	configuration, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return errors.New("Kubernetes in-cluster configuration is unavailable")
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return errors.New("create AgentTeams Kubernetes client")
	}
	coreClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.New("create Kubernetes Core client")
	}
	publisher, err := agentteamsbinding.NewFileBindingPublisher(configuration.bindingRoot)
	if err != nil {
		return fmt.Errorf("open runtime binding publisher: %w", err)
	}
	controller := agentteamsbinding.Controller{
		Dynamic: dynamicClient, Core: coreClient, Namespace: configuration.namespace, ControllerName: configuration.controllerName,
		AllowedSecretNames: configuration.allowedSecretNames, RuntimeBindings: stateRuntimeBindingReader{state: eventStateReader{events: readOnlyEventStore{store: eventstore.New(configuration.governanceRoot)}}},
		Bindings: publisher, BindingTTL: configuration.bindingTTL,
	}
	return controller.Run(ctx)
}

func loadConfig(getenv func(string) string) (config, error) {
	if getenv == nil {
		return config{}, errors.New("environment reader is required")
	}
	result := config{
		namespace: strings.TrimSpace(getenv(envNamespace)), controllerName: strings.TrimSpace(getenv(envControllerName)),
		governanceRoot: strings.TrimSpace(getenv(envGovernanceRoot)), bindingRoot: strings.TrimSpace(getenv(envBindingRoot)),
	}
	for _, name := range strings.Split(getenv(envAllowedSecrets), ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result.allowedSecretNames = append(result.allowedSecretNames, name)
		}
	}
	result.bindingTTL, _ = time.ParseDuration(strings.TrimSpace(getenv(envBindingTTL)))
	if result.namespace == "" || result.controllerName == "" || result.governanceRoot == "" || result.bindingRoot == "" ||
		len(result.allowedSecretNames) == 0 || result.bindingTTL <= 0 || result.bindingTTL > agentteamsbinding.MaxBindingTTL {
		return config{}, errors.New("binding namespace, roots, allowed Secret names, and bounded TTL are required")
	}
	for _, name := range result.allowedSecretNames {
		if strings.ContainsAny(name, "*?[]") {
			return config{}, errors.New("binding Secret names must be exact")
		}
	}
	return result, nil
}

type eventReader interface {
	ReadAll(context.Context) ([]model.Event, error)
}

type eventStateReader struct{ events eventReader }

type readOnlyEventStore struct{ store eventstore.Store }

func (reader readOnlyEventStore) ReadAll(ctx context.Context) ([]model.Event, error) {
	return reader.store.ReadAllReadOnly(ctx)
}

func (reader eventStateReader) State(ctx context.Context) (model.ProjectState, error) {
	if reader.events == nil {
		return model.ProjectState{}, errors.New("governance event reader is unavailable")
	}
	events, err := reader.events.ReadAll(ctx)
	if err != nil {
		return model.ProjectState{}, err
	}
	return model.Reduce(events)
}

type stateReader interface {
	State(context.Context) (model.ProjectState, error)
}

type stateRuntimeBindingReader struct{ state stateReader }

func (reader stateRuntimeBindingReader) CurrentRuntimeBinding(ctx context.Context, logicalActorID string) (model.RuntimeBinding, error) {
	if reader.state == nil || strings.TrimSpace(logicalActorID) == "" {
		return model.RuntimeBinding{}, errors.New("runtime binding reader is unavailable")
	}
	state, err := reader.state.State(ctx)
	if err != nil {
		return model.RuntimeBinding{}, err
	}
	matches := make([]model.RuntimeBinding, 0, 1)
	for _, binding := range state.RuntimeBindings[logicalActorID] {
		if binding.Status == "active" {
			matches = append(matches, binding)
		}
	}
	if len(matches) == 0 {
		return model.RuntimeBinding{}, agentteamsbinding.ErrRuntimeBindingNotFound
	}
	if len(matches) != 1 {
		return model.RuntimeBinding{}, errors.New("exactly one active RuntimeBinding is required")
	}
	if matches[0].LogicalActorID != logicalActorID {
		return model.RuntimeBinding{}, errors.New("active RuntimeBinding does not match requested logical actor")
	}
	return matches[0], nil
}

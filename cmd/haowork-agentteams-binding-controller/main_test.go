package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbinding"
	"github.com/haochase/haowork/internal/model"
)

func TestLoadConfigRequiresExplicitRuntimeIdentityInputs(t *testing.T) {
	values := map[string]string{
		envNamespace: "haowork-public", envControllerName: "haowork-public-agentteams-controller",
		envGovernanceRoot: "/var/lib/haowork/governance", envBindingRoot: "/var/lib/haowork/bindings",
		envAllowedSecrets: "agentteams-creds-haowork-msn-p005-v122-real-delivery-leader", envBindingTTL: "2m",
	}
	config, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.namespace != "haowork-public" || config.controllerName != "haowork-public-agentteams-controller" ||
		config.governanceRoot != "/var/lib/haowork/governance" || config.bindingRoot != "/var/lib/haowork/bindings" ||
		len(config.allowedSecretNames) != 1 || config.bindingTTL.String() != "2m0s" {
		t.Fatalf("config = %#v", config)
	}
	delete(values, envControllerName)
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("missing controller name was accepted")
	}
}

func TestLoadConfigRejectsBroadOrExcessiveCredentialAccess(t *testing.T) {
	values := map[string]string{
		envNamespace: "haowork-public", envControllerName: "haowork-public-agentteams-controller",
		envGovernanceRoot: "/var/lib/haowork/governance", envBindingRoot: "/var/lib/haowork/bindings",
		envAllowedSecrets: "agentteams-creds-*", envBindingTTL: "6m",
	}
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("wildcard Secret access and excessive binding TTL were accepted")
	}
}

func TestStateRuntimeBindingReaderRequiresExactlyOneActiveBinding(t *testing.T) {
	binding := model.RuntimeBinding{LogicalActorID: "AGT-LEADER", Revision: 2, Status: "active"}
	reader := stateRuntimeBindingReader{state: staticStateReader{state: model.ProjectState{RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-LEADER": {binding}}}}}
	got, err := reader.CurrentRuntimeBinding(context.Background(), "AGT-LEADER")
	if err != nil || got.Revision != 2 {
		t.Fatalf("binding=%#v err=%v", got, err)
	}
	reader.state = staticStateReader{state: model.ProjectState{RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-LEADER": {binding, binding}}}}
	if _, err := reader.CurrentRuntimeBinding(context.Background(), "AGT-LEADER"); err == nil || errors.Is(err, agentteamsbinding.ErrRuntimeBindingNotFound) {
		t.Fatal("multiple active bindings were accepted")
	}
	wrongActor := binding
	wrongActor.LogicalActorID = "AGT-OTHER"
	reader.state = staticStateReader{state: model.ProjectState{RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-LEADER": {binding, wrongActor}}}}
	if _, err := reader.CurrentRuntimeBinding(context.Background(), "AGT-LEADER"); err == nil || errors.Is(err, agentteamsbinding.ErrRuntimeBindingNotFound) {
		t.Fatal("active binding stored under the wrong actor key was ignored")
	}
}

func TestStateRuntimeBindingReaderReportsNotFoundWhenNoActiveBindingExists(t *testing.T) {
	reader := stateRuntimeBindingReader{state: staticStateReader{state: model.ProjectState{}}}
	if _, err := reader.CurrentRuntimeBinding(context.Background(), "AGT-LEADER"); !errors.Is(err, agentteamsbinding.ErrRuntimeBindingNotFound) {
		t.Fatalf("error = %v, want ErrRuntimeBindingNotFound", err)
	}
}

func TestStateRuntimeBindingReaderPropagatesStateReadError(t *testing.T) {
	readErr := errors.New("governance store unavailable")
	reader := stateRuntimeBindingReader{state: staticStateReader{err: readErr}}
	if _, err := reader.CurrentRuntimeBinding(context.Background(), "AGT-LEADER"); !errors.Is(err, readErr) || errors.Is(err, agentteamsbinding.ErrRuntimeBindingNotFound) {
		t.Fatalf("error = %v, want state read error", err)
	}
}

type staticStateReader struct {
	state model.ProjectState
	err   error
}

func (reader staticStateReader) State(context.Context) (model.ProjectState, error) {
	return reader.state, reader.err
}

func TestBindingControllerManifestsIsolateBindingPVCAndCredentialRBAC(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2")
	for _, name := range []string{"haowork-binding-controller.yaml", "haowork-binding-controller-internal.yaml"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		secretName := "agentteams-creds-haowork-msn-p005-v122-real-delivery-leader"
		if strings.Contains(name, "-internal") {
			secretName = "agentteams-creds-haowork-msn-p005-v122-internal-delivery-leader"
		}
		for _, required := range []string{
			"replicas: 1", "name: haowork-runtime-bindings", "claimName: haowork-runtime-bindings",
			"claimName: haowork-governance-state", "mountPath: /var/lib/haowork/governance", "readOnly: true",
			"mountPath: /var/lib/haowork/bindings", "readOnly: false",
			`resourceNames: ["` + secretName + `"]`,
			"HAOWORK_BINDING_ALLOWED_SECRET_NAMES", "HAOWORK_BINDING_TTL", "value: 2m",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omits %q", name, required)
			}
		}
		secretRule := `resources: ["secrets"]`
		secretRuleIndex := strings.Index(text, secretRule)
		if secretRuleIndex < 0 {
			t.Fatalf("%s omits the Secret rule", name)
		}
		resourceNamesIndex := strings.Index(text[secretRuleIndex:], "resourceNames:")
		verbsIndex := strings.Index(text[secretRuleIndex:], `verbs: ["get"]`)
		if resourceNamesIndex < 0 || verbsIndex < 0 || resourceNamesIndex > verbsIndex {
			t.Fatalf("%s Secret get is not restricted by resourceNames", name)
		}
	}
	for _, name := range []string{"haowork-mcp.yaml", "haowork-mcp-internal.yaml"} {
		content, _ := os.ReadFile(filepath.Join(root, name))
		text := string(content)
		for _, required := range []string{
			"replicas: 1", "claimName: haowork-governance-state", "/var/lib/haowork/project",
			"claimName: haowork-runtime-bindings", "mountPath: /var/lib/haowork/bindings", "readOnly: true",
			"/var/lib/haowork/bindings/mcp-bindings.json",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omits %q", name, required)
			}
		}
		for _, forbidden := range []string{"haowork-mcp-runtime-bindings", "/var/run/secrets/haowork-mcp"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s retains obsolete Secret binding mount %q", name, forbidden)
			}
		}
	}
	for _, name := range []string{"haowork-core-bridge.yaml", "haowork-core-bridge-internal.yaml"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "haowork-runtime-bindings") || strings.Contains(string(content), "/var/lib/haowork/bindings") {
			t.Fatalf("%s mounts the isolated binding PVC", name)
		}
	}
}

func TestBindingControllerKubectlAuthorizationContractIsReadOnlyAndNamed(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2")
	for _, name := range []string{"haowork-binding-controller.yaml", "haowork-binding-controller-internal.yaml"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		secretName := "agentteams-creds-haowork-msn-p005-v122-real-delivery-leader"
		if strings.Contains(name, "-internal") {
			secretName = "agentteams-creds-haowork-msn-p005-v122-internal-delivery-leader"
		}
		if strings.Count(text, "resourceNames:") != 1 || strings.Count(text, secretName) != 2 {
			t.Fatalf("%s does not encode one exact Secret authorization target", name)
		}
		for _, forbidden := range []string{"higress-console", `verbs: ["list"]`, `verbs: ["watch"]`, `verbs: ["create"]`, `verbs: ["update"]`, `verbs: ["patch"]`, `verbs: ["delete"]`} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s violates the kubectl auth can-i expectation with %q", name, forbidden)
			}
		}
	}
}

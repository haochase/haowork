package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresCompleteProductionDependencies(t *testing.T) {
	t.Setenv("HAOWORK_CORE_BRIDGE_LISTEN_ADDR", "0.0.0.0:8081")
	t.Setenv("HAOWORK_CORE_BRIDGE_STATE_ROOT", t.TempDir())
	t.Setenv("HAOWORK_CORE_BRIDGE_TOKEN", "bridge-token")
	t.Setenv("HAOWORK_CORE_PROJECT_ID", "PRJ-P005-V122")
	t.Setenv("HAOWORK_CORE_ENVIRONMENT_ID", "public")
	t.Setenv("HAOWORK_CORE_NAMESPACE", "haowork-public")
	t.Setenv("HAOWORK_CORE_CONTROLLER_NAME", "haowork-public-agentteams-controller")
	t.Setenv("HAOWORK_CORE_MATRIX_URL", "http://haowork-public-agentteams-tuwunel.haowork-public.svc.cluster.local:8008")
	t.Setenv("HAOWORK_CORE_MATRIX_TOKEN", "matrix-token")
	t.Setenv("HAOWORK_CORE_S3_ENDPOINT", "haowork-public-agentteams-minio.haowork-public.svc.cluster.local:9000")
	t.Setenv("HAOWORK_CORE_S3_ACCESS_KEY", "default")
	t.Setenv("HAOWORK_CORE_S3_SECRET_KEY", "minio-secret")
	t.Setenv("HAOWORK_CORE_S3_BUCKET", "haowork-public-artifacts")
	t.Setenv("HAOWORK_CORE_HIGRESS_CONSOLE_URL", "http://higress-console.haowork-public.svc.cluster.local:8080")
	t.Setenv("HAOWORK_CORE_HIGRESS_USERNAME", "admin")
	t.Setenv("HAOWORK_CORE_HIGRESS_PASSWORD", "console-secret")
	t.Setenv("HAOWORK_CORE_MCP_SERVER_NAME", "haowork-mcp")
	t.Setenv("HAOWORK_CORE_MCP_CONSUMER_NAME", "manager")
	t.Setenv("HAOWORK_CORE_MCP_ROUTE_NAME", "haowork-mcp")
	t.Setenv("HAOWORK_CORE_MODEL", "model")
	t.Setenv("HAOWORK_CORE_MANAGER_RUNTIME", "openclaw")
	t.Setenv("HAOWORK_CORE_WORKER_RUNTIME", "openclaw")
	t.Setenv("HAOWORK_CORE_MCP_URL", "http://haowork-mcp.haowork-public.svc.cluster.local:8080/mcp")
	t.Setenv("HAOWORK_CORE_MCP_TRANSPORT", "http")
	t.Setenv("HAOWORK_CORE_HUMAN_NAME", "owner")

	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.environmentID != "public" || config.namespace != "haowork-public" || config.matrixToken == "" || config.s3SecretKey == "" || config.higressPassword == "" {
		t.Fatalf("config is incomplete: %#v", config.redacted())
	}
}

func TestLoadConfigFailsClosedWithoutSecrets(t *testing.T) {
	for _, name := range requiredEnvironmentNames() {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig succeeded without mounted production dependencies")
	}
}

func TestDeploymentManifestUsesSecretReferencesAndLeastPrivilegeRuntime(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2", "haowork-core-bridge.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, required := range []string{
		"kind: PersistentVolumeClaim", "kind: ClusterRole", "kind: ClusterRoleBinding",
		"name: haowork-core-bridge", "type: ClusterIP", "imagePullPolicy: Never",
		"secretKeyRef:", "haowork-core-bridge-runtime", "key: matrix-token", "key: bucket", "haowork-public-agentteams-minio", "higress-console",
		"runAsNonRoot: true", "readOnlyRootFilesystem: true", "automountServiceAccountToken: true",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("deployment manifest omits %q", required)
		}
	}
	for _, forbidden := range []string{"kind: Secret", "replace-with", "stringData:", "hostNetwork: true", "type: LoadBalancer", "WORKER_MATRIX_TOKEN", "agentteams-storage"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("deployment manifest contains forbidden %q", forbidden)
		}
	}
}

func TestInternalDeploymentManifestsKeepGovernanceRuntimeIndependent(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2")
	core, err := os.ReadFile(filepath.Join(root, "haowork-core-bridge-internal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := os.ReadFile(filepath.Join(root, "haowork-mcp-internal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	coreText, mcpText := string(core), string(mcp)
	for _, required := range []string{
		"kind: PersistentVolumeClaim", "kind: Role", "kind: RoleBinding", "namespace: haowork-internal",
		"kind: ClusterRole", "kind: ClusterRoleBinding", "name: haowork-core-bridge-discovery",
		"resources: [\"customresourcedefinitions\"]", "verbs: [\"get\", \"list\", \"watch\"]",
		"name: haowork-core-bridge-runtime", "HAOWORK_CORE_ENVIRONMENT_ID", "value: internal",
		"haowork-internal-agentteams-tuwunel", "haowork-internal-agentteams-minio",
		"kind: Service", "name: haowork-core-bridge",
	} {
		if !strings.Contains(coreText, required) {
			t.Fatalf("internal Core Bridge manifest omits %q", required)
		}
	}
	for _, required := range []string{
		"kind: Service", "name: haowork-mcp", "haowork-governance-state",
		"haowork-mcp-runtime-bindings", "HAOWORK_MCP_USE_CORE_BRIDGE_STATE",
	} {
		if !strings.Contains(mcpText, required) {
			t.Fatalf("internal MCP manifest omits %q", required)
		}
	}
	for _, forbidden := range []string{"haowork-public", "kind: Secret", "hostNetwork: true", "type: LoadBalancer"} {
		if strings.Contains(coreText, forbidden) || strings.Contains(mcpText, forbidden) {
			t.Fatalf("internal manifests contain forbidden %q", forbidden)
		}
	}
}

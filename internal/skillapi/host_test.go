package skillapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeConsumerAuthenticatorParsesDigestOnlyBindings(t *testing.T) {
	const gatewayKey = "higress-worker-build-key"
	digest := sha256.Sum256([]byte(gatewayKey))
	authenticator, err := ParseRuntimeConsumerAuthenticator(strings.NewReader(`{
  "bindings": [{
    "consumer_name": "worker-build",
    "credential_sha256": "` + hex.EncodeToString(digest[:]) + `",
    "principal": {
      "logical_actor_id": "AGT-BUILD",
      "runtime_principal_id": "runtime-build",
      "environment_id": "public",
      "agentteams_instance_id": "AT-001",
      "binding_revision": 2
    }
  }]
}`))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+gatewayKey)
	principal, err := authenticator.Authenticate(context.Background(), request)
	if err != nil || principal.LogicalActorID != "AGT-BUILD" || principal.RuntimePrincipalID != "runtime-build" {
		t.Fatalf("parsed principal = %#v, err=%v", principal, err)
	}
	if _, err := ParseRuntimeConsumerAuthenticator(strings.NewReader(`{"bindings":[],"gateway_key":"must-not-be-accepted"}`)); err == nil {
		t.Fatal("runtime consumer bindings accepted an unknown plaintext credential field")
	}
}

func TestMCPHostAuthenticatesOfficialRuntimePrincipal(t *testing.T) {
	server, adapter := testServer(t, true)
	const gatewayKey = "higress-worker-build-key"
	digest := sha256.Sum256([]byte(gatewayKey))
	authenticator, err := NewRuntimeConsumerAuthenticator([]RuntimeConsumerBinding{{
		ConsumerName:     "worker-build",
		CredentialSHA256: hex.EncodeToString(digest[:]),
		Principal: RuntimePrincipal{
			LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", BindingRevision: 2,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server.Authenticator = authenticator
	host, err := NewHost(HostConfig{ListenAddress: "127.0.0.1:18090", Server: server})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}, "meta": testCallMeta(),
	})))
	request.Header.Set("Authorization", "Bearer "+gatewayKey)
	request.Header.Set("X-Haowork-Logical-Actor-ID", "AGT-SPOOFED")
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || adapter.calls != 1 {
		t.Fatalf("MCP host response = %d %s; calls=%d", response.Code, response.Body.String(), adapter.calls)
	}
	if adapter.invocation.LogicalActorID != "AGT-BUILD" || adapter.invocation.RuntimePrincipalID != "runtime-build" || adapter.invocation.EnvironmentID != "public" || adapter.invocation.AgentTeamsInstanceID != "AT-001" || adapter.invocation.RuntimeBindingRevision != 2 {
		t.Fatalf("MCP host trusted a caller parameter instead of runtime identity: %#v", adapter.invocation)
	}
	unauthenticated := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcpRequest(t, "initialize", map[string]any{})))
	unauthenticated.Header.Set("Authorization", "Bearer forged-key")
	denied := httptest.NewRecorder()
	host.Handler().ServeHTTP(denied, unauthenticated)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("forged gateway credential status = %d", denied.Code)
	}
	if _, err := NewHost(HostConfig{ListenAddress: ":18090", Server: server}); err == nil {
		t.Fatal("MCP host accepted an implicit listen address")
	}
}

func TestMCPHostSupportsValidatedGatewayMountPath(t *testing.T) {
	server, adapter := testServer(t, true)
	const gatewayKey = "higress-manager-key"
	digest := sha256.Sum256([]byte(gatewayKey))
	authenticator, err := NewRuntimeConsumerAuthenticator([]RuntimeConsumerBinding{{
		ConsumerName: "manager", CredentialSHA256: hex.EncodeToString(digest[:]),
		Principal: RuntimePrincipal{LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", BindingRevision: 2},
	}})
	if err != nil {
		t.Fatal(err)
	}
	server.Authenticator = authenticator
	path := "/mcp-servers/haowork-mcp/mcp"
	host, err := NewHost(HostConfig{ListenAddress: "127.0.0.1:18090", Server: server, RoutePath: path})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}, "meta": testCallMeta(),
	})))
	request.Header.Set("Authorization", "Bearer "+gatewayKey)
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || adapter.calls != 1 {
		t.Fatalf("gateway route response = %d %s; calls=%d", response.Code, response.Body.String(), adapter.calls)
	}
	backendRequest := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}, "meta": testCallMeta(),
	})))
	backendRequest.Header.Set("Authorization", "Bearer "+gatewayKey)
	backendResponse := httptest.NewRecorder()
	host.Handler().ServeHTTP(backendResponse, backendRequest)
	if backendResponse.Code != http.StatusOK || adapter.calls != 2 {
		t.Fatalf("rewritten backend route response = %d %s; calls=%d", backendResponse.Code, backendResponse.Body.String(), adapter.calls)
	}
	if _, err := NewHost(HostConfig{ListenAddress: "127.0.0.1:18090", Server: server, RoutePath: "/mcp-servers/../mcp"}); err == nil {
		t.Fatal("MCP host accepted an unsafe gateway route path")
	}
}

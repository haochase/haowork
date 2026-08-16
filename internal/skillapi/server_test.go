package skillapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

func TestMCPRejectsUnauthenticatedBeforeJSONDecode(t *testing.T) {
	server := &Server{Authenticator: rejectAuthenticator{}}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{"))
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMCPInitializeAndToolsListExposeRoleScopedCanonicalSchemas(t *testing.T) {
	server, _ := testServer(t, false)

	initialize := mcpRequest(t, "initialize", map[string]any{})
	response := serveMCP(server, initialize)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"protocolVersion"`)) {
		t.Fatalf("initialize = %d %s", response.Code, response.Body.String())
	}

	response = serveMCP(server, mcpRequest(t, "tools/list", map[string]any{}))
	if response.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d: %s", response.Code, response.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte("haowork.patch")) || bytes.Contains(encoded, []byte("haowork.verify")) || bytes.Contains(encoded, []byte("haowork.import")) {
		t.Fatalf("Build tool scope = %s", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"inputSchema"`)) || !bytes.Contains(encoded, []byte(`"outputSchema"`)) {
		t.Fatalf("canonical schemas missing from tools/list: %s", encoded)
	}
}

func TestMCPToolsCallBindsAuthenticatedRuntimeIdentity(t *testing.T) {
	server, adapter := testServer(t, true)
	response := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{
		"name":      "haowork.patch",
		"arguments": map[string]any{"patch_sha256": "patch"},
		"meta":      testCallMeta(),
	}))
	if response.Code != http.StatusOK || adapter.calls != 1 {
		t.Fatalf("tools/call = %d %s; adapter calls = %d", response.Code, response.Body.String(), adapter.calls)
	}
	if adapter.invocation.LogicalActorID != "AGT-BUILD" || adapter.invocation.RuntimeBindingRevision != 2 || adapter.invocation.EnvironmentID != "public" || adapter.invocation.RuntimePrincipalID != "runtime-build" || adapter.invocation.AgentTeamsInstanceID != "AT-001" {
		t.Fatalf("bound invocation = %#v", adapter.invocation)
	}
}

func TestMCPToolsCallRejectsArgumentsOutsideRegistrySchema(t *testing.T) {
	server, adapter := testServer(t, true)
	response := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch",
		"arguments": map[string]any{
			"patch_sha256": "patch", "logical_actor_id": "AGT-SPOOFED", "environment_id": "spoofed",
		},
		"meta": testCallMeta(),
	}))
	if response.Code != http.StatusBadRequest || adapter.calls != 0 || !bytes.Contains(response.Body.Bytes(), []byte("invalid_input")) {
		t.Fatalf("schema response = %d %s; calls=%d", response.Code, response.Body.String(), adapter.calls)
	}
}

func TestMCPToolsCallRejectsUnregisteredWorkItemOrTrace(t *testing.T) {
	server, adapter := testServer(t, true)
	meta := testCallMeta()
	meta["trace_id"] = "TRC-UNREGISTERED"
	response := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}, "meta": meta,
	}))
	if response.Code != http.StatusForbidden || adapter.calls != 0 || !bytes.Contains(response.Body.Bytes(), []byte("denied")) {
		t.Fatalf("binding response = %d %s; calls=%d", response.Code, response.Body.String(), adapter.calls)
	}
}

func TestMCPToolsCallReturnsApprovalRequiredWithoutInvokingAdapter(t *testing.T) {
	server, adapter := testServer(t, false)
	response := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{
		"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}, "meta": testCallMeta(),
	}))
	if response.Code != http.StatusOK || adapter.calls != 0 || !bytes.Contains(response.Body.Bytes(), []byte("approval_required")) {
		t.Fatalf("approval response = %d %s; calls=%d", response.Code, response.Body.String(), adapter.calls)
	}
}

func TestMCPWriteToolDoesNotRetryRedirectOrDecodeError(t *testing.T) {
	server, adapter := testServer(t, true)
	response := serveMCP(server, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`))
	if response.Code != http.StatusBadRequest || adapter.calls != 0 || response.Header().Get("Location") != "" {
		t.Fatalf("invalid write response = %d %s; calls=%d location=%q", response.Code, response.Body.String(), adapter.calls, response.Header().Get("Location"))
	}
}

func TestMCPRejectsOversizedBodyUnknownFieldsAndBatchRequests(t *testing.T) {
	server, _ := testServer(t, false)
	server.MaxBodyBytes = 64
	for name, payload := range map[string][]byte{
		"oversized": bytes.Repeat([]byte("x"), 65),
		"unknown":   []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","unexpected":true}`),
		"batch":     []byte(`[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`),
	} {
		t.Run(name, func(t *testing.T) {
			response := serveMCP(server, payload)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
		})
	}
}

type testAuthenticator struct{ principal RuntimePrincipal }

func (auth testAuthenticator) Authenticate(context.Context, *http.Request) (RuntimePrincipal, error) {
	return auth.principal, nil
}

type rejectAuthenticator struct{}

func (rejectAuthenticator) Authenticate(context.Context, *http.Request) (RuntimePrincipal, error) {
	return RuntimePrincipal{}, ErrUnauthenticated
}

type captureAdapter struct {
	calls      int
	invocation skillruntime.Invocation
}

func (adapter *captureAdapter) Invoke(_ context.Context, invocation skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	adapter.calls++
	adapter.invocation = invocation
	return json.RawMessage(`{"change_id":"CHG-001"}`), nil, nil
}

type acceptingAudit struct{}

func (acceptingAudit) RecordSkillCall(context.Context, skillruntime.Invocation, skillruntime.Result) error {
	return nil
}

type acceptingTrace struct{}

func (acceptingTrace) PolicyDecision(context.Context, skillruntime.Invocation, skillruntime.Decision) error {
	return nil
}
func (acceptingTrace) ApprovalWait(context.Context, skillruntime.Invocation, skillruntime.Decision) error {
	return nil
}
func (acceptingTrace) AdapterStarted(context.Context, skillruntime.Invocation, skillruntime.Decision) error {
	return nil
}
func (acceptingTrace) AdapterFinished(context.Context, skillruntime.Invocation, skillruntime.Decision, skillruntime.Result) error {
	return nil
}
func (acceptingTrace) AuditResult(context.Context, skillruntime.Invocation, skillruntime.Decision, skillruntime.Result, error) error {
	return nil
}
func (acceptingTrace) Promote(context.Context, skillruntime.Invocation, skillruntime.Decision, skillruntime.Result) error {
	return nil
}

func testServer(t *testing.T, approved bool) (*Server, *captureAdapter) {
	t.Helper()
	registry, err := skillruntime.Load(skillDefinitionsRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"patch_sha256":"patch"}`)
	digest := sha256.Sum256(input)
	state := model.ProjectState{
		Goal:            model.GoalVersion{Version: 1},
		Agents:          map[string]model.LogicalAgent{"AGT-BUILD": {ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active"}},
		RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-BUILD": {{LogicalActorID: "AGT-BUILD", Revision: 2, EnvironmentID: "public", RuntimePrincipalID: "runtime-build", AgentTeamsInstanceID: "AT-001", Status: "active"}}},
		Missions:        map[string]model.MissionEnvelope{"MSN-001": {ID: "MSN-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "context-hash", LeaseID: "LSE-001", AllowedScopes: []string{"internal/skillapi"}, AllowedSkills: []model.MissionSkillGrant{{Name: "patch", Version: "1.0.0"}}, EnvironmentID: "public", RiskLevel: "L2"}},
		Contexts:        map[string]model.ContextSlice{"CTX-001": {ID: "CTX-001", TaskID: "TSK-001", GoalVersion: 1, SliceHash: "context-hash"}},
		Leases:          map[string]model.Lease{"LSE-001": {ID: "LSE-001", TaskID: "TSK-001", SubjectID: "AGT-BUILD", EnvironmentID: "public", ContextID: "CTX-001", GoalVersion: 1, AllowedScopes: []string{"internal/skillapi"}, AllowedSkills: []string{"patch"}, Status: "active", StartsAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Hour)}},
		Runs:            map[string]model.Run{"RUN-001": {ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "context-hash", Status: model.StatusRunning}},
		Approvals:       map[string]model.ApprovalRequest{},
	}
	if approved {
		state.Approvals["APR-001"] = model.ApprovalRequest{ID: "APR-001", SubjectType: "skill", SubjectID: "patch", PayloadSHA256: hex.EncodeToString(digest[:]), RiskLevel: "L2", RequesterID: "AGT-BUILD", DeciderID: "USR-LEAD", Status: "approved"}
	}
	adapter := &captureAdapter{}
	runtime := &skillruntime.Runtime{Policy: skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)}, Adapter: adapter, Audit: acceptingAudit{}, Tracer: acceptingTrace{}}
	return &Server{Registry: registry, Runtime: runtime, BindingReader: BindingReaderFunc(func(_ context.Context, invocation skillruntime.Invocation) error {
		if invocation.WorkItemID != "WKI-001" || invocation.TraceID != "TRC-001" {
			return ErrBindingNotFound
		}
		return nil
	}), Authenticator: testAuthenticator{principal: RuntimePrincipal{LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", BindingRevision: 2}}}, adapter
}

func testCallMeta() map[string]any {
	return map[string]any{"id": "INV-001", "mission_id": "MSN-001", "task_id": "TSK-001", "work_item_id": "WKI-001", "run_id": "RUN-001", "trace_id": "TRC-001", "goal_version": 1, "context_id": "CTX-001", "context_hash": "context-hash", "lease_id": "LSE-001", "scope": []string{"internal/skillapi"}}
}

func mcpRequest(t *testing.T, method string, params any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func serveMCP(server *Server, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func skillDefinitionsRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "skills")
}

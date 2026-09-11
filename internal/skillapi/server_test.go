package skillapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/trace"
)

func TestReceiptToolResolvesInvocationFromAssignmentWithoutCustomMeta(t *testing.T) {
	server, store := receiptTestServer(t, receiptTestPrincipal())
	response := serveMCP(server, receiptToolRequest(t, map[string]any{}))
	if response.Code != http.StatusOK {
		t.Fatalf("receipt tools/call = %d %s", response.Code, response.Body.String())
	}
	result := parseCallToolResult(t, response)
	wantKeys := map[string]bool{"receipt_id": true, "assignment_id": true, "accepted_as_candidate": true, "receipt_sha256": true}
	if result.isError || len(result.payload) != len(wantKeys) {
		t.Fatalf("receipt response = %#v", result)
	}
	for key := range result.payload {
		if !wantKeys[key] {
			t.Fatalf("receipt response leaked field %q: %#v", key, result.payload)
		}
	}
	if result.payload["assignment_id"] != "ASN-46fc8933c009dd44765c6f4b01aeaf9d" || result.payload["accepted_as_candidate"] != true {
		t.Fatalf("receipt response = %#v", result.payload)
	}
	receipt, err := store.Receipt(context.Background(), "ASN-46fc8933c009dd44765c6f4b01aeaf9d")
	if err != nil || receipt.AssignmentID != "ASN-46fc8933c009dd44765c6f4b01aeaf9d" {
		t.Fatalf("stored receipt = %#v, err=%v", receipt, err)
	}
	receiptSHA256, ok := result.payload["receipt_sha256"].(string)
	if !ok || len(receiptSHA256) != 64 {
		t.Fatalf("receipt_sha256 = %#v", result.payload["receipt_sha256"])
	}
	if _, err := hex.DecodeString(receiptSHA256); err != nil || receiptSHA256 != strings.TrimPrefix(receipt.ID, "ERC-") || receiptSHA256 == receipt.ReportSHA256 {
		t.Fatalf("receipt_sha256 = %q, receipt=%#v, decode=%v", receiptSHA256, receipt, err)
	}
}

func TestReceiptToolIgnoresIdentityFieldsInsideArguments(t *testing.T) {
	server, _ := receiptTestServer(t, receiptTestPrincipal())
	response := serveMCP(server, receiptToolRequest(t, map[string]any{
		"mission_id": "MSN-FORGED", "run_id": "RUN-FORGED", "work_item_id": "WKI-FORGED",
		"environment_id": "internal", "logical_actor_id": "AGT-FORGED", "runtime_binding_revision": 99,
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("forged arguments response = %d %s", response.Code, response.Body.String())
	}
	assertRPCErrorData(t, response, "receipt_schema_invalid", false)
}

func TestReceiptToolRejectsAssignmentOwnedByAnotherLeader(t *testing.T) {
	principal := receiptTestPrincipal()
	principal.LogicalActorID = "AGT-OTHER-LEADER"
	principal.RuntimePrincipalID = "runtime-other-leader"
	server, store := receiptTestServer(t, principal)
	response := serveMCP(server, receiptToolRequest(t, map[string]any{}))
	if response.Code != http.StatusOK {
		t.Fatalf("foreign leader response = %d %s", response.Code, response.Body.String())
	}
	assertRPCErrorData(t, response, "principal_mismatch", false)
	if _, err := store.Receipt(context.Background(), "ASN-46fc8933c009dd44765c6f4b01aeaf9d"); !errors.Is(err, executionreceipt.ErrReceiptNotFound) {
		t.Fatalf("foreign leader persisted receipt: %v", err)
	}
}

func TestReceiptToolPreservesAssignmentAndBindingDomainErrors(t *testing.T) {
	for name, test := range map[string]struct {
		principal    RuntimePrincipal
		assignmentID string
		code         string
	}{
		"missing assignment": {principal: receiptTestPrincipal(), assignmentID: "ASN-00000000000000000000000000000000", code: "assignment_not_found"},
		"binding revision": func() struct {
			principal    RuntimePrincipal
			assignmentID string
			code         string
		} {
			principal := receiptTestPrincipal()
			principal.BindingRevision++
			return struct {
				principal    RuntimePrincipal
				assignmentID string
				code         string
			}{principal: principal, assignmentID: "ASN-46fc8933c009dd44765c6f4b01aeaf9d", code: "binding_revision_mismatch"}
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := receiptTestServer(t, test.principal)
			response := serveMCP(server, receiptToolRequest(t, map[string]any{"assignment_id": test.assignmentID}))
			assertRPCErrorData(t, response, test.code, false)
		})
	}
}

func TestReceiptToolMapsFiniteDomainErrorsWithRetryability(t *testing.T) {
	for name, test := range map[string]struct {
		stage     string
		err       error
		code      string
		retryable bool
	}{
		"assignment inactive": {stage: "binding", err: executionreceipt.ErrAssignmentInactive, code: "assignment_inactive"},
		"lease invalid":       {stage: "binding", err: executionreceipt.ErrLeaseInvalid, code: "lease_invalid"},
		"receipt conflict":    {stage: "adapter", err: executionreceipt.ErrReceiptConflict, code: "receipt_conflict"},
		"schema invalid":      {stage: "adapter", err: executionreceipt.ErrReceiptSchemaInvalid, code: "receipt_schema_invalid"},
		"store failure":       {stage: "resolver", err: executionreceipt.ErrReceiptStoreFailed, code: "receipt_store_failed", retryable: true},
		"trace failure":       {stage: "adapter", err: executionreceipt.ErrReceiptStoreFailed, code: "receipt_store_failed", retryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := receiptTestServer(t, receiptTestPrincipal())
			switch test.stage {
			case "resolver":
				server.InvocationResolvers["submit_execution_report"] = errorInvocationResolver{err: test.err}
			case "binding":
				server.BindingReader = BindingReaderFunc(func(context.Context, skillruntime.Invocation) error { return test.err })
			case "adapter":
				server.Runtime.Adapter = errorAdapter{err: test.err}
			}
			response := serveMCP(server, receiptToolRequest(t, map[string]any{}))
			assertRPCErrorData(t, response, test.code, test.retryable)
		})
	}
}

func TestToolsListShowsReceiptOnlyToDeliveryLeader(t *testing.T) {
	leaderServer, _ := receiptTestServer(t, receiptTestPrincipal())
	leader := serveMCP(leaderServer, mcpRequest(t, "tools/list", map[string]any{}))
	if leader.Code != http.StatusOK || !bytes.Contains(leader.Body.Bytes(), []byte("haowork.submit_execution_report")) {
		t.Fatalf("leader tools/list = %d %s", leader.Code, leader.Body.String())
	}

	build := receiptTestPrincipal()
	build.LogicalActorID = "AGT-BUILD"
	build.RuntimePrincipalID = "runtime-build"
	buildServer, _ := receiptTestServer(t, build)
	buildResponse := serveMCP(buildServer, mcpRequest(t, "tools/list", map[string]any{}))
	if buildResponse.Code != http.StatusOK || bytes.Contains(buildResponse.Body.Bytes(), []byte("haowork.submit_execution_report")) {
		t.Fatalf("build tools/list = %d %s", buildResponse.Code, buildResponse.Body.String())
	}
}

func TestCustomResolverDoesNotBypassRuntimePolicyForLegacySkill(t *testing.T) {
	server, adapter := testServer(t, false)
	definition, err := server.Registry.Resolve("patch", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"patch_sha256":"patch"}`)
	server.InvocationResolvers = map[string]InvocationResolver{
		"patch": staticInvocationResolver{invocation: bindInvocation(invocationMeta{
			ID: "INV-001", MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "WKI-001",
			RunID: "RUN-001", TraceID: "TRC-001", GoalVersion: 1, ContextID: "CTX-001",
			ContextHash: "context-hash", LeaseID: "LSE-001", Scope: []string{"internal/skillapi"},
		}, receiptTestPrincipalForBuild(), definition, input)},
	}
	response := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{"name": "haowork.patch", "arguments": map[string]any{"patch_sha256": "patch"}}))
	if response.Code != http.StatusOK || adapter.calls != 0 || !bytes.Contains(response.Body.Bytes(), []byte("approval_required")) {
		t.Fatalf("legacy resolver bypassed runtime policy: status=%d calls=%d body=%s", response.Code, adapter.calls, response.Body.String())
	}
}

func TestInvocationContextUsesSkillTimeoutAndEarlierUpstreamDeadline(t *testing.T) {
	started := time.Now()
	ctx, cancel, err := invocationContext(context.Background(), skillruntime.Definition{Timeout: "20ms"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("skill timeout was not applied")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("skill timeout took %s", elapsed)
	}

	upstream, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	ctx, cancel, err = invocationContext(upstream, skillruntime.Definition{Timeout: "500ms"})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("earlier upstream deadline was not preserved")
	}
}

func TestReceiptToolTimeoutCoversResolverBindingAndAdapter(t *testing.T) {
	for _, stage := range []string{"resolver", "binding", "adapter"} {
		t.Run(stage, func(t *testing.T) {
			server, _ := receiptTestServer(t, receiptTestPrincipal())
			switch stage {
			case "resolver":
				server.InvocationResolvers["submit_execution_report"] = blockingInvocationResolver{}
			case "binding":
				server.BindingReader = BindingReaderFunc(func(ctx context.Context, _ skillruntime.Invocation) error {
					<-ctx.Done()
					return ctx.Err()
				})
			case "adapter":
				server.Runtime.Adapter = blockingAdapter{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			started := time.Now()
			response := serveMCPContext(server, ctx, receiptToolRequest(t, map[string]any{}))
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("%s timeout took %s", stage, elapsed)
			}
			assertRPCErrorData(t, response, "receipt_store_failed", true)
		})
	}
}

func TestMCPRejectsUnauthenticatedBeforeJSONDecode(t *testing.T) {
	server := &Server{Authenticator: rejectAuthenticator{}}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{"))
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	assertJSONRPCError(t, response, -32001, "denied")
}

func TestReceiptProtocolBoundaryErrorsRemainJSONRPCErrors(t *testing.T) {
	server, _ := receiptTestServer(t, receiptTestPrincipal())
	unknown := serveMCP(server, mcpRequest(t, "tools/call", map[string]any{"name": "haowork.unknown", "arguments": map[string]any{}}))
	assertJSONRPCError(t, unknown, -32602, "invalid_input")

	malformed := serveMCP(server, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`))
	assertJSONRPCError(t, malformed, -32600, "invalid_input")

	server, _ = receiptTestServer(t, receiptTestPrincipal())
	server.Runtime = nil
	unavailable := serveMCP(server, receiptToolRequest(t, map[string]any{}))
	assertInternalJSONRPCError(t, unavailable)
}

func TestReceiptServiceConfigurationErrorsRemainInternalJSONRPCErrors(t *testing.T) {
	for _, stage := range []string{"invalid timeout", "resolver missing", "binding reader missing", "adapter missing"} {
		t.Run(stage, func(t *testing.T) {
			server, _ := receiptTestServer(t, receiptTestPrincipal())
			switch stage {
			case "invalid timeout":
				registry := receiptRegistryWithTimeout(t, "not-a-duration")
				server.Registry = registry
				server.Runtime.Policy.Registry = registry
			case "resolver missing":
				delete(server.InvocationResolvers, "submit_execution_report")
			case "binding reader missing":
				server.BindingReader = nil
			case "adapter missing":
				server.Runtime.Adapter = nil
			}
			response := serveMCP(server, receiptToolRequest(t, map[string]any{}))
			assertInternalJSONRPCError(t, response)
			for _, forbidden := range []string{"resolver", "binding", "adapter", "duration", "not-a-duration"} {
				if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
					t.Fatalf("configuration error leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
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
	result := parseCallToolResult(t, response)
	if result.isError || result.payload["change_id"] != "CHG-001" {
		t.Fatalf("legacy skill result = %#v", result)
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
	result := parseCallToolResult(t, response)
	if !result.isError || result.payload["code"] != "approval_required" {
		t.Fatalf("approval CallToolResult = %#v", result)
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

type staticInvocationResolver struct{ invocation skillruntime.Invocation }

func (resolver staticInvocationResolver) ResolveInvocation(context.Context, RuntimePrincipal, skillruntime.Definition, json.RawMessage) (skillruntime.Invocation, error) {
	return resolver.invocation, nil
}

type errorInvocationResolver struct{ err error }

func (resolver errorInvocationResolver) ResolveInvocation(context.Context, RuntimePrincipal, skillruntime.Definition, json.RawMessage) (skillruntime.Invocation, error) {
	return skillruntime.Invocation{}, resolver.err
}

type blockingInvocationResolver struct{}

func (blockingInvocationResolver) ResolveInvocation(ctx context.Context, _ RuntimePrincipal, _ skillruntime.Definition, _ json.RawMessage) (skillruntime.Invocation, error) {
	<-ctx.Done()
	return skillruntime.Invocation{}, ctx.Err()
}

type errorAdapter struct{ err error }

func (adapter errorAdapter) Invoke(context.Context, skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	return nil, nil, adapter.err
}

type blockingAdapter struct{}

func (blockingAdapter) Invoke(ctx context.Context, _ skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
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
	return serveMCPContext(server, context.Background(), body)
}

func serveMCPContext(server *Server, ctx context.Context, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body)).WithContext(ctx)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

type callToolResult struct {
	isError bool
	payload map[string]any
}

func parseCallToolResult(t *testing.T, response *httptest.ResponseRecorder) callToolResult {
	t.Helper()
	var document struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || document.JSONRPC != jsonRPCVersion || document.Error != nil || len(document.Result) == 0 {
		t.Fatalf("response is not a JSON-RPC success envelope: %d %s", response.Code, response.Body.String())
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(document.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result["content"] == nil || result["isError"] == nil || result["structuredContent"] != nil {
		t.Fatalf("invalid CallToolResult keys: %s", document.Result)
	}
	var isError bool
	if err := json.Unmarshal(result["isError"], &isError); err != nil {
		t.Fatal(err)
	}
	var content []map[string]json.RawMessage
	if err := json.Unmarshal(result["content"], &content); err != nil || len(content) != 1 || len(content[0]) != 2 {
		t.Fatalf("invalid CallToolResult content: %s, err=%v", result["content"], err)
	}
	var contentType, text string
	if err := json.Unmarshal(content[0]["type"], &contentType); err != nil || contentType != "text" {
		t.Fatalf("content type = %q, err=%v", contentType, err)
	}
	if err := json.Unmarshal(content[0]["text"], &text); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("content text is not JSON: %q: %v", text, err)
	}
	return callToolResult{isError: isError, payload: payload}
}

func assertRPCErrorData(t *testing.T, response *httptest.ResponseRecorder, code string, retryable bool) {
	t.Helper()
	result := parseCallToolResult(t, response)
	gotRetryable, exists := result.payload["retryable"].(bool)
	if !result.isError || len(result.payload) != 2 || result.payload["code"] != code || !exists || gotRetryable != retryable {
		t.Fatalf("tool error = %#v, want code=%q retryable=%t", result, code, retryable)
	}
}

func assertJSONRPCError(t *testing.T, response *httptest.ResponseRecorder, rpcCode int, detail string) {
	t.Helper()
	var document struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Error == nil || len(document.Result) != 0 || document.Error.Code != rpcCode {
		t.Fatalf("JSON-RPC error = %#v, result=%s, response=%s", document.Error, document.Result, response.Body.String())
	}
	data, ok := document.Error.Data.(map[string]any)
	if !ok || data["code"] != detail {
		t.Fatalf("JSON-RPC error data = %#v, want %q", document.Error.Data, detail)
	}
}

func assertInternalJSONRPCError(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertJSONRPCError(t, response, -32603, "denied")
	if response.Code != http.StatusInternalServerError || !bytes.Contains(response.Body.Bytes(), []byte(`"message":"internal error"`)) {
		t.Fatalf("internal JSON-RPC error = %d %s", response.Code, response.Body.String())
	}
}

func receiptRegistryWithTimeout(t *testing.T, timeout string) *skillruntime.Registry {
	t.Helper()
	source := skillDefinitionsRoot(t)
	root := t.TempDir()
	err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(root, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == "submit_execution_report.yaml" {
			data = []byte(strings.Replace(string(data), "timeout: 30s", "timeout: "+timeout, 1))
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := skillruntime.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func skillDefinitionsRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "skills")
}

func receiptTestServer(t *testing.T, principal RuntimePrincipal) (*Server, executionreceipt.Store) {
	t.Helper()
	registry, err := skillruntime.Load(skillDefinitionsRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := executionreceipt.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	assignment := executionreceipt.ExecutionAssignment{
		ID: "ASN-46fc8933c009dd44765c6f4b01aeaf9d", ProjectID: "PRJ-001", MissionID: "MSN-001", GovernanceTaskID: "TSK-001",
		RunID: "RUN-001", WorkItemID: "WKI-001", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001",
		LeaderLogicalActorID: "AGT-LEADER", LeaderRuntimePrincipalID: "runtime-leader", AssignmentRevision: 1,
		RuntimeBindingRevision: 2, GoalVersion: 1, ContextID: "CTX-001", ContextHash: strings.Repeat("c", 64),
		LeaseID: "LSE-001", MissionArtifactURI: "s3://haowork/environments/public/missions/MSN-001/artifacts/" + strings.Repeat("d", 64),
		MissionArtifactSHA256: strings.Repeat("d", 64), MatrixRoomID: "!leader:example.test",
		State: executionreceipt.AssignmentPrepared, CreatedAt: time.Now().UTC().Add(-time.Minute), Deadline: time.Now().UTC().Add(time.Hour),
	}
	if _, err := store.Register(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDispatched(context.Background(), assignment.ID, "$dispatch"); err != nil {
		t.Fatal(err)
	}
	state := model.ProjectState{
		Agents: map[string]model.LogicalAgent{
			"AGT-LEADER": {ID: "AGT-LEADER", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionDeliveryLeader, Status: "active"},
			"AGT-BUILD":  {ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active"},
		},
	}
	service := executionreceipt.Service{Store: store, Trace: trace.New(root), Clock: func() time.Time { return time.Now().UTC() }}
	adapters := AdapterMap{"submit_execution_report": ExecutionReceiptAdapter{Service: service}}
	runtime := &skillruntime.Runtime{Policy: skillruntime.Policy{Registry: registry, State: skillruntime.StaticState(state)}, Adapter: adapters, Audit: acceptingAudit{}, Tracer: acceptingTrace{}}
	server := &Server{
		Registry: registry, Runtime: runtime, BindingReader: BindingReaderFunc(func(context.Context, skillruntime.Invocation) error { return nil }),
		Authenticator:       testAuthenticator{principal: principal},
		InvocationResolvers: map[string]InvocationResolver{"submit_execution_report": ExecutionReceiptInvocationResolver{Store: store}},
	}
	return server, store
}

func receiptTestPrincipal() RuntimePrincipal {
	return RuntimePrincipal{LogicalActorID: "AGT-LEADER", RuntimePrincipalID: "runtime-leader", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", BindingRevision: 2}
}

func receiptTestPrincipalForBuild() RuntimePrincipal {
	return RuntimePrincipal{LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", BindingRevision: 2}
}

func receiptToolRequest(t *testing.T, extra map[string]any) []byte {
	t.Helper()
	arguments := map[string]any{
		"assignment_id": "ASN-46fc8933c009dd44765c6f4b01aeaf9d", "status": "succeeded", "summary_sha256": strings.Repeat("a", 64),
		"artifacts": []any{}, "checks": []any{},
	}
	for key, value := range extra {
		arguments[key] = value
	}
	return mcpRequest(t, "tools/call", map[string]any{"name": "haowork.submit_execution_report", "arguments": arguments})
}

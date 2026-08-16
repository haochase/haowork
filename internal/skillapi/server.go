package skillapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

var ErrUnauthenticated = errors.New("skill request is unauthenticated")
var ErrBindingNotFound = errors.New("invocation binding is not registered")

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (RuntimePrincipal, error)
}

// RuntimePrincipal is supplied by the transport authenticator, never by MCP arguments.
type RuntimePrincipal struct {
	LogicalActorID, RuntimePrincipalID, EnvironmentID, AgentTeamsInstanceID string
	BindingRevision                                                         int
}

// BindingReader resolves execution-owned work-item and trace registrations before a call reaches Runtime.
type BindingReader interface {
	ValidateInvocation(context.Context, skillruntime.Invocation) error
}

type BindingReaderFunc func(context.Context, skillruntime.Invocation) error

func (fn BindingReaderFunc) ValidateInvocation(ctx context.Context, invocation skillruntime.Invocation) error {
	return fn(ctx, invocation)
}

type Server struct {
	Registry      *skillruntime.Registry
	Runtime       *skillruntime.Runtime
	BindingReader BindingReader
	Authenticator Authenticator
	MaxBodyBytes  int64
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(server.handle)
}

func (server *Server) handle(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeRPCError(response, http.StatusMethodNotAllowed, nil, -32600, "invalid request", "invalid_input")
		return
	}
	if server.Authenticator == nil {
		writeRPCError(response, http.StatusUnauthorized, nil, -32001, "unauthenticated", "denied")
		return
	}
	principal, err := server.Authenticator.Authenticate(request.Context(), request)
	if err != nil {
		writeRPCError(response, http.StatusUnauthorized, nil, -32001, "unauthenticated", "denied")
		return
	}
	call, err := decodeRPCRequest(response, request, server.maxBodyBytes())
	if err != nil {
		writeRPCError(response, http.StatusBadRequest, nil, -32600, "invalid request", "invalid_input")
		return
	}
	if call.JSONRPC != jsonRPCVersion || strings.TrimSpace(call.Method) == "" {
		writeRPCError(response, http.StatusBadRequest, call.ID, -32600, "invalid request", "invalid_input")
		return
	}

	switch call.Method {
	case "initialize":
		if server.Registry == nil {
			writeRPCError(response, http.StatusServiceUnavailable, call.ID, -32000, "skill registry unavailable", "denied")
			return
		}
		writeRPCResult(response, http.StatusOK, call.ID, map[string]any{
			"protocolVersion": "2025-03-26",
			"serverInfo":      map[string]string{"name": "haowork-governance-skills"},
			"capabilities":    map[string]any{"tools": map[string]bool{}},
		})
	case "notifications/initialized":
		response.WriteHeader(http.StatusNoContent)
	case "tools/list":
		server.handleToolsList(response, request.Context(), call.ID, principal)
	case "tools/call":
		server.handleToolsCall(response, request.Context(), call.ID, principal, call.Params)
	default:
		writeRPCError(response, http.StatusBadRequest, call.ID, -32601, "method not found", "invalid_input")
	}
}

func (server *Server) handleToolsList(response http.ResponseWriter, ctx context.Context, id json.RawMessage, principal RuntimePrincipal) {
	if server.Registry == nil || server.Runtime == nil || server.Runtime.Policy.State == nil {
		writeRPCError(response, http.StatusServiceUnavailable, id, -32000, "skill runtime unavailable", "denied")
		return
	}
	state, err := server.Runtime.Policy.State.State(ctx)
	if err != nil {
		writeRPCError(response, http.StatusServiceUnavailable, id, -32000, "skill state unavailable", "denied")
		return
	}
	agent, exists := state.Agents[principal.LogicalActorID]
	if !exists || agent.Status != "active" {
		writeRPCError(response, http.StatusForbidden, id, -32002, "skill access denied", "denied")
		return
	}
	tools := make([]tool, 0)
	for _, definition := range server.Registry.Definitions() {
		if !allowsFunction(definition.AllowedFunctions, agent.Function) {
			continue
		}
		tools = append(tools, tool{
			Name:         "haowork." + strings.ToLower(definition.Name),
			Description:  definition.Adapter,
			InputSchema:  definition.InputSchema,
			OutputSchema: definition.OutputSchema,
		})
	}
	writeRPCResult(response, http.StatusOK, id, map[string]any{"tools": tools})
}

func (server *Server) handleToolsCall(response http.ResponseWriter, ctx context.Context, id json.RawMessage, principal RuntimePrincipal, raw json.RawMessage) {
	if server.Registry == nil || server.Runtime == nil {
		writeRPCError(response, http.StatusServiceUnavailable, id, -32000, "skill runtime unavailable", "denied")
		return
	}
	var call callParams
	if err := decodeStrict(raw, &call); err != nil || !json.Valid(call.Arguments) {
		writeRPCError(response, http.StatusBadRequest, id, -32602, "invalid tool arguments", "invalid_input")
		return
	}
	skillName, ok := strings.CutPrefix(strings.TrimSpace(call.Name), "haowork.")
	if !ok || skillName == "" {
		writeRPCError(response, http.StatusBadRequest, id, -32602, "invalid tool name", "invalid_input")
		return
	}
	definition, err := server.Registry.Resolve(strings.ToLower(skillName), "")
	if err != nil {
		writeRPCError(response, http.StatusBadRequest, id, -32602, "unknown tool", "invalid_input")
		return
	}
	if err := skillruntime.ValidateJSONInput(definition, call.Arguments); err != nil {
		writeRPCError(response, http.StatusBadRequest, id, -32602, "invalid tool arguments", "invalid_input")
		return
	}
	if strings.TrimSpace(call.Meta.WorkItemID) == "" || strings.TrimSpace(call.Meta.RunID) == "" || strings.TrimSpace(call.Meta.TraceID) == "" {
		writeRPCError(response, http.StatusBadRequest, id, -32602, "missing invocation binding", "invalid_input")
		return
	}
	invocation := bindInvocation(call.Meta, principal, definition, call.Arguments)
	if server.BindingReader == nil {
		writeRPCError(response, http.StatusServiceUnavailable, id, -32000, "invocation binding reader unavailable", "denied")
		return
	}
	if err := server.BindingReader.ValidateInvocation(ctx, invocation); err != nil {
		writeRPCError(response, http.StatusForbidden, id, -32002, "invocation binding is not registered", "denied")
		return
	}
	result, err := server.Runtime.Invoke(ctx, invocation)
	if err != nil {
		writeRPCError(response, http.StatusBadRequest, id, -32003, "skill invocation failed", "invalid_input")
		return
	}
	if result.Status == skillruntime.ResultRejected {
		code := "denied"
		if result.ErrorCode == skillruntime.CodeApprovalRequired {
			code = "approval_required"
		}
		writeRPCResult(response, http.StatusOK, id, map[string]any{"isError": true, "code": code, "result": result})
		return
	}
	if result.Status != skillruntime.ResultSucceeded {
		writeRPCResult(response, http.StatusOK, id, map[string]any{"isError": true, "code": "invalid_input", "result": result})
		return
	}
	writeRPCResult(response, http.StatusOK, id, map[string]any{"isError": false, "structuredContent": json.RawMessage(result.Output), "result": result})
}

func bindInvocation(meta invocationMeta, principal RuntimePrincipal, definition skillruntime.Definition, input json.RawMessage) skillruntime.Invocation {
	digest := sha256.Sum256(input)
	return skillruntime.Invocation{
		ID: meta.ID, MissionID: meta.MissionID, TaskID: meta.TaskID, WorkItemID: meta.WorkItemID, RunID: meta.RunID,
		LogicalActorID: principal.LogicalActorID, RuntimePrincipalID: principal.RuntimePrincipalID, AgentTeamsInstanceID: principal.AgentTeamsInstanceID, RuntimeBindingRevision: principal.BindingRevision,
		SkillName: definition.Name, SkillVersion: definition.Version, EnvironmentID: principal.EnvironmentID, TraceID: meta.TraceID,
		GoalVersion: meta.GoalVersion, ContextID: meta.ContextID, ContextHash: meta.ContextHash, LeaseID: meta.LeaseID,
		Scope: append([]string(nil), meta.Scope...), Input: append(json.RawMessage(nil), input...), InputSHA256: hex.EncodeToString(digest[:]),
	}
}

func allowsFunction(functions []model.AgentFunction, candidate model.AgentFunction) bool {
	for _, function := range functions {
		if function == candidate {
			return true
		}
	}
	return false
}

func (server *Server) maxBodyBytes() int64 {
	if server.MaxBodyBytes > 0 {
		return server.MaxBodyBytes
	}
	return defaultMaxBody
}

func decodeRPCRequest(response http.ResponseWriter, request *http.Request, limit int64) (rpcRequest, error) {
	var call rpcRequest
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&call); err != nil {
		return rpcRequest{}, err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return rpcRequest{}, errors.New("multiple JSON-RPC requests are not allowed")
		}
		return rpcRequest{}, err
	}
	return call, nil
}

func decodeStrict(raw json.RawMessage, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func writeRPCResult(response http.ResponseWriter, status int, id json.RawMessage, result any) {
	writeRPC(response, status, rpcResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

func writeRPCError(response http.ResponseWriter, status int, id json.RawMessage, code int, message, detail string) {
	writeRPC(response, status, rpcResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: message, Data: map[string]string{"code": detail}}})
}

func writeRPC(response http.ResponseWriter, status int, value rpcResponse) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

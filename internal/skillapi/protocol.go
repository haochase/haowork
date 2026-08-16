// Package skillapi exposes governed skills through a constrained MCP-compatible JSON-RPC surface.
package skillapi

import "encoding/json"

const (
	jsonRPCVersion = "2.0"
	defaultMaxBody = 1 << 20
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      invocationMeta  `json:"meta"`
}

type invocationMeta struct {
	ID          string   `json:"id"`
	MissionID   string   `json:"mission_id"`
	TaskID      string   `json:"task_id"`
	WorkItemID  string   `json:"work_item_id"`
	RunID       string   `json:"run_id"`
	TraceID     string   `json:"trace_id"`
	GoalVersion int      `json:"goal_version"`
	ContextID   string   `json:"context_id"`
	ContextHash string   `json:"context_hash"`
	LeaseID     string   `json:"lease_id"`
	Scope       []string `json:"scope"`
}

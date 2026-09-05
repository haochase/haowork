package corebridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

type Starter interface {
	Start(context.Context, executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error)
}

type Factory func(model.MissionEnvelope) (Starter, error)

type Config struct {
	Token       string
	State       *State
	Factory     Factory
	RunTimeout  time.Duration
	MaxBodySize int64
	ReportError func(string, error)
}

type Server struct {
	token       string
	state       *State
	factory     Factory
	runTimeout  time.Duration
	maxBodySize int64
	reportError func(string, error)
}

type StartRequest struct {
	Mission model.MissionEnvelope           `json:"mission"`
	Request executor.AgentTeamsStartRequest `json:"request"`
}

type RunEvidence struct {
	RunID                  string        `json:"run_id"`
	RuntimeBindingRevision int           `json:"runtime_binding_revision"`
	Cursor                 string        `json:"cursor"`
	SourceEventIDs         []string      `json:"source_event_ids"`
	CoreHistorySHA256      string        `json:"core_history_sha256"`
	TraceSHA256            string        `json:"trace_sha256"`
	TraceIDs               []string      `json:"trace_ids"`
	Artifacts              []RunArtifact `json:"artifacts"`
}

type RunArtifact struct {
	Kind          string `json:"kind"`
	Key           string `json:"key"`
	SHA256        string `json:"sha256"`
	EnvironmentID string `json:"environment_id"`
	Size          int64  `json:"size"`
}

func NewServer(config Config) (*Server, error) {
	if strings.TrimSpace(config.Token) == "" || config.State == nil || config.Factory == nil {
		return nil, errors.New("Core Bridge token, state, and production transport factory are required")
	}
	if config.RunTimeout <= 0 {
		config.RunTimeout = 45 * time.Second
	}
	if config.MaxBodySize <= 0 {
		config.MaxBodySize = 1 << 20
	}
	return &Server{token: config.Token, state: config.State, factory: config.Factory, runTimeout: config.RunTimeout, maxBodySize: config.MaxBodySize, reportError: config.ReportError}, nil
}

func (server *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if !server.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/ready":
		writeJSON(response, http.StatusOK, map[string]bool{
			"mission_resolver_ready": true, "runtime_binding_store_ready": true,
			"trace_store_ready": true, "production_transport_ready": true,
		})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/runs/start":
		server.start(response, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/runs/") && strings.HasSuffix(request.URL.Path, "/evidence"):
		runID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/runs/"), "/evidence")
		evidence, err := server.state.Evidence(request.Context(), runID)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "evidence_not_found"})
			return
		}
		writeJSON(response, http.StatusOK, evidence)
	default:
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (server *Server) start(response http.ResponseWriter, request *http.Request) {
	var input StartRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, server.maxBodySize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if input.Request.MissionID != input.Mission.ID || input.Request.RunID == "" || input.Request.WorkItemID == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "binding_mismatch"})
		return
	}
	if err := server.state.RegisterMission(request.Context(), input.Mission); err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"error": "mission_rejected"})
		return
	}
	if previous, exists := server.state.RunRequest(input.Request.RunID); exists && reflect.DeepEqual(previous, input.Request) {
		evidence, evidenceErr := server.state.Evidence(request.Context(), input.Request.RunID)
		if evidenceErr != nil {
			server.report("existing_run_evidence", evidenceErr)
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "evidence_read_failed"})
			return
		}
		writeJSON(response, http.StatusOK, evidence)
		return
	}
	starter, err := server.factory(input.Mission)
	if err != nil {
		server.report("transport_factory", err)
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "transport_unavailable"})
		return
	}
	runContext, cancel := context.WithTimeout(request.Context(), server.runTimeout)
	defer cancel()
	session, err := starter.Start(runContext, input.Request)
	if err != nil {
		server.report("transport_start", err)
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": "transport_start_failed"})
		return
	}
	if bound, ok := session.(executor.BoundAgentTeamsSession); ok {
		boundRequest := bound.BoundRequest()
		if boundRequest.RunID != input.Request.RunID || boundRequest.MissionID != input.Request.MissionID || boundRequest.LogicalActorID != input.Request.LogicalActorID || boundRequest.RuntimeBindingRevision < 1 {
			_ = session.Cancel(context.Background())
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": "bound_request_invalid"})
			return
		}
		input.Request = boundRequest
	}
	cursor := server.state.Cursor(input.Request.RunID)
	var eventIDs []string
	for event := range session.Events(runContext, cursor) {
		if event.RunID != input.Request.RunID || event.SourceEventID == "" || event.WorkspaceDigest == "" {
			_ = session.Cancel(context.Background())
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": "event_binding_failed"})
			return
		}
		eventIDs = append(eventIDs, event.SourceEventID)
		if event.AdapterCursor != "" {
			cursor = event.AdapterCursor
		}
	}
	var terminalErr error
	if source, ok := session.(interface {
		Errors(context.Context) <-chan error
	}); ok {
		select {
		case terminalErr = <-source.Errors(runContext):
		default:
		}
	}
	if terminalErr != nil {
		server.report("transport_events", terminalErr)
		failure := map[string]string{"error": "event_stream_failed", "reason": safeSessionFailureCode(terminalErr)}
		status := http.StatusBadGateway
		if len(eventIDs) == 0 {
			failure["error"] = "no_governed_event"
			status = http.StatusGatewayTimeout
		}
		writeJSON(response, status, failure)
		return
	}
	if len(eventIDs) == 0 {
		writeJSON(response, http.StatusGatewayTimeout, map[string]string{"error": "no_governed_event"})
		return
	}
	if err := server.state.RecordRun(input.Request, eventIDs, cursor); err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "evidence_persist_failed"})
		return
	}
	evidence, err := server.state.Evidence(request.Context(), input.Request.RunID)
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "evidence_read_failed"})
		return
	}
	writeJSON(response, http.StatusOK, evidence)
}

func safeSessionFailureCode(err error) string {
	if coded, ok := err.(interface{ SafeCode() string }); ok {
		switch coded.SafeCode() {
		case "matrix_leader_event_unmatched", "matrix_leader_response_missing":
			return coded.SafeCode()
		}
	}
	return "event_stream_failed"
}

func (server *Server) report(stage string, err error) {
	if server.reportError != nil && err != nil {
		server.reportError(stage, err)
	}
}

func (server *Server) authorized(request *http.Request) bool {
	provided := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if provided == "" || len(provided) != len(server.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(server.token)) == 1
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

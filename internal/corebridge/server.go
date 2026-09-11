package corebridge

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/executionreceipt"
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
	runMu       sync.Mutex
	inflight    map[string]*runReservation
}

type runReservation struct {
	request       executor.AgentTeamsStartRequest
	missionDigest string
	done          chan struct{}
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
	ExecutionReceiptIDs    []string      `json:"execution_receipt_ids"`
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

type CandidateTraceExistence struct {
	RunID                string `json:"run_id"`
	CandidateTraceExists bool   `json:"candidate_trace_exists"`
}

type ReceiptExistence struct {
	AssignmentID  string `json:"assignment_id"`
	ReceiptExists bool   `json:"receipt_exists"`
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
	return &Server{token: config.Token, state: config.State, factory: config.Factory, runTimeout: config.RunTimeout, maxBodySize: config.MaxBodySize, reportError: config.ReportError, inflight: make(map[string]*runReservation)}, nil
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
		if request.URL.RawPath != "" || ValidateRunID(runID) != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_run_id"})
			return
		}
		evidence, err := server.state.Evidence(request.Context(), runID)
		if err != nil {
			writeJSON(response, http.StatusNotFound, map[string]string{"error": "evidence_not_found"})
			return
		}
		writeJSON(response, http.StatusOK, evidence)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/runs/") && strings.HasSuffix(request.URL.Path, "/candidate-trace-existence"):
		runID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/runs/"), "/candidate-trace-existence")
		if request.URL.RawPath != "" || ValidateRunID(runID) != nil {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_run_id"})
			return
		}
		exists, err := server.state.CandidateTraceExists(request.Context(), runID)
		if err != nil {
			server.report("candidate_trace_read", err)
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "trace_read_failed"})
			return
		}
		writeJSON(response, http.StatusOK, CandidateTraceExistence{RunID: runID, CandidateTraceExists: exists})
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/assignments/") && strings.HasSuffix(request.URL.Path, "/receipt-existence"):
		assignmentID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1/assignments/"), "/receipt-existence")
		if request.URL.RawPath != "" || !executionreceipt.ValidAssignmentID(assignmentID) {
			writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_assignment_id"})
			return
		}
		exists, err := server.state.ReceiptExists(request.Context(), assignmentID)
		if err != nil {
			server.report("receipt_read", err)
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "receipt_read_failed"})
			return
		}
		writeJSON(response, http.StatusOK, ReceiptExistence{AssignmentID: assignmentID, ReceiptExists: exists})
	default:
		writeJSON(response, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (server *Server) start(response http.ResponseWriter, request *http.Request) {
	var input StartRequest
	content, err := io.ReadAll(io.LimitReader(request.Body, server.maxBodySize+1))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if int64(len(content)) > server.maxBodySize {
		writeJSON(response, http.StatusRequestEntityTooLarge, map[string]string{"error": "request_too_large"})
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
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
	if err := ValidateRunID(input.Request.RunID); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_run_id"})
		return
	}
	if input.Request.MissionID != input.Mission.ID || input.Request.WorkItemID == "" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "binding_mismatch"})
		return
	}
	missionDigest, err := canonicalMissionDigest(input.Mission)
	if err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"error": "mission_rejected"})
		return
	}
	requireNew, err := parseRequireNew(request.Header.Values("X-Haowork-Require-New"))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	reservation, status := server.reserveRun(request.Context(), input.Request, missionDigest, requireNew)
	switch status {
	case runReservationAlreadyExists:
		writeJSON(response, http.StatusConflict, map[string]string{"error": "run_already_exists"})
		return
	case runReservationConflict:
		writeJSON(response, http.StatusConflict, map[string]string{"error": "run_conflict"})
		return
	case runReservationCanceled:
		writeJSON(response, http.StatusRequestTimeout, map[string]string{"error": "run_wait_canceled"})
		return
	}
	defer server.releaseRun(input.Request.RunID, reservation)
	if previous, exists := server.state.RunRequest(input.Request.RunID); exists {
		if !sameImmutableStartRequest(previous, input.Request) {
			writeJSON(response, http.StatusConflict, map[string]string{"error": "run_conflict"})
			return
		}
		if err := server.state.RegisterMission(request.Context(), input.Mission); err != nil {
			writeJSON(response, http.StatusConflict, map[string]string{"error": "mission_rejected"})
			return
		}
		evidence, evidenceErr := server.state.Evidence(request.Context(), input.Request.RunID)
		if evidenceErr != nil {
			if errors.Is(evidenceErr, ErrLegacyRunEvidence) {
				writeJSON(response, http.StatusConflict, map[string]string{"error": "run_upgrade_required"})
				return
			}
			server.report("existing_run_evidence", evidenceErr)
			writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "evidence_read_failed"})
			return
		}
		writeJSON(response, http.StatusOK, evidence)
		return
	}
	if err := server.state.RegisterMission(request.Context(), input.Mission); err != nil {
		writeJSON(response, http.StatusConflict, map[string]string{"error": "mission_rejected"})
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
		if !validBoundRequest(input.Request, boundRequest) {
			_ = session.Cancel(context.Background())
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": "bound_request_invalid"})
			return
		}
		input.Request = boundRequest
	}
	cursor := server.state.Cursor(input.Request.RunID)
	var eventIDs []string
	var receiptIDs []string
	for event := range session.Events(runContext, cursor) {
		if event.RunID != input.Request.RunID || event.Kind != "execution_receipt" || event.SourceEventID == "" || event.WorkspaceDigest == "" {
			_ = session.Cancel(context.Background())
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": "event_binding_failed"})
			return
		}
		eventIDs = append(eventIDs, event.SourceEventID)
		receiptIDs = append(receiptIDs, event.SourceEventID)
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
	if len(receiptIDs) != 1 {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": "event_binding_failed"})
		return
	}
	if err := server.state.RecordRun(input.Request, eventIDs, receiptIDs, cursor); err != nil {
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

func parseRequireNew(values []string) (bool, error) {
	if len(values) == 0 {
		return false, nil
	}
	if len(values) != 1 {
		return false, errors.New("ambiguous require-new header")
	}
	switch values[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("invalid require-new header")
	}
}

type runReservationStatus int

const (
	runReservationOwned runReservationStatus = iota
	runReservationAlreadyExists
	runReservationConflict
	runReservationCanceled
)

func (server *Server) reserveRun(ctx context.Context, request executor.AgentTeamsStartRequest, missionDigest string, requireNew bool) (*runReservation, runReservationStatus) {
	for {
		server.runMu.Lock()
		active, exists := server.inflight[request.RunID]
		if requireNew {
			_, persisted := server.state.RunRequest(request.RunID)
			if persisted || exists {
				server.runMu.Unlock()
				return nil, runReservationAlreadyExists
			}
			reservation := &runReservation{request: request, missionDigest: missionDigest, done: make(chan struct{})}
			server.inflight[request.RunID] = reservation
			server.runMu.Unlock()
			return reservation, runReservationOwned
		}
		if !exists {
			reservation := &runReservation{request: request, missionDigest: missionDigest, done: make(chan struct{})}
			server.inflight[request.RunID] = reservation
			server.runMu.Unlock()
			return reservation, runReservationOwned
		}
		if active.missionDigest != missionDigest || !sameImmutableStartRequest(active.request, request) {
			server.runMu.Unlock()
			return nil, runReservationConflict
		}
		done := active.done
		server.runMu.Unlock()
		select {
		case <-done:
			continue
		case <-ctx.Done():
			return nil, runReservationCanceled
		}
	}
}

func canonicalMissionDigest(mission model.MissionEnvelope) (string, error) {
	canonical, err := model.CanonicalizeMissionEnvelope(mission)
	if err != nil || strings.TrimSpace(mission.Hash) == "" || mission.Hash != canonical.Hash {
		return "", errors.New("Mission canonical hash is invalid")
	}
	return canonical.Hash, nil
}

func (server *Server) releaseRun(runID string, reservation *runReservation) {
	server.runMu.Lock()
	if server.inflight[runID] == reservation {
		delete(server.inflight, runID)
		close(reservation.done)
	}
	server.runMu.Unlock()
}

func sameImmutableStartRequest(left, right executor.AgentTeamsStartRequest) bool {
	left.RuntimePrincipalID = ""
	left.RuntimeBindingRevision = 0
	right.RuntimePrincipalID = ""
	right.RuntimeBindingRevision = 0
	return reflect.DeepEqual(left, right)
}

func validBoundRequest(original, bound executor.AgentTeamsStartRequest) bool {
	if strings.TrimSpace(bound.RuntimePrincipalID) == "" || bound.RuntimeBindingRevision < 1 {
		return false
	}
	expected := original
	expected.RuntimePrincipalID = bound.RuntimePrincipalID
	expected.RuntimeBindingRevision = bound.RuntimeBindingRevision
	return reflect.DeepEqual(expected, bound)
}

func safeSessionFailureCode(err error) string {
	if coded, ok := err.(interface{ SafeCode() string }); ok {
		switch coded.SafeCode() {
		case "matrix_leader_event_unmatched", "matrix_leader_response_missing", "execution_report_missing", "execution_report_invalid":
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

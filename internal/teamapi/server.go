package teamapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

const (
	teamHealthPath = "/_haowork/team/health"
	teamAPIPath    = "/api/v1/team/"
)

type TeamService interface {
	Pull(context.Context, uint64) ([]model.Event, error)
	Push(context.Context, team.Principal, team.PushBatch) (team.PushResult, error)
	Status(context.Context, team.Principal) (team.Status, error)
	ProposeGoalChange(context.Context, team.Principal, model.GoalChange, ...string) (team.PushResult, error)
	ApproveGoalChange(context.Context, team.Principal, string) (team.PushResult, error)
	RejectGoalChange(context.Context, team.Principal, string, string) (team.PushResult, error)
	IssueLease(context.Context, team.Principal, model.Lease) (team.PushResult, error)
	RenewLease(context.Context, team.Principal, string, time.Time) (team.PushResult, error)
	ReleaseLease(context.Context, team.Principal, string) (team.PushResult, error)
	RevokeLease(context.Context, team.Principal, string) (team.PushResult, error)
	ResolveConflict(context.Context, team.Principal, team.ConflictResolutionRequest) (team.PushResult, error)
}

type Server struct {
	ProjectID       string
	ProtocolVersion string
	Service         TeamService
	Authenticator   Authenticator
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type healthResponse struct {
	ProjectID       string `json:"project_id"`
	ProtocolVersion string `json:"protocol_version"`
}

type eventsResponse struct {
	Events []model.Event `json:"events"`
}

type renewLeaseRequest struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type rejectGoalChangeRequest struct {
	Reason string `json:"reason"`
}
type resolveConflictRequest struct {
	Action      string        `json:"action"`
	Replacement []model.Event `json:"replacement,omitempty"`
	Confirmed   bool          `json:"confirmed"`
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(teamHealthPath, server.handleHealth)
	mux.HandleFunc(teamAPIPath, server.handleAPI)
	mux.HandleFunc(strings.TrimSuffix(teamAPIPath, "/"), server.handleAPI)
	return mux
}

// Serve permits plain HTTP only on a loopback listener. TLS configuration is
// supplied by callers; no insecure TLS escape hatch exists in this package.
func (server *Server) Serve(listener net.Listener, certificateFile, keyFile string) error {
	if listener == nil {
		return errors.New("team listener is required")
	}
	plain := certificateFile == "" && keyFile == ""
	if (certificateFile == "") != (keyFile == "") {
		return errors.New("team TLS certificate and key must be supplied together")
	}
	if plain && !listenerIsLoopback(listener) {
		return errors.New("non-loopback Team Core listener requires TLS")
	}
	if plain {
		return http.Serve(listener, server.Handler())
	}
	return http.ServeTLS(listener, server.Handler(), certificateFile, keyFile)
}

func (server *Server) handleHealth(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	version := server.ProtocolVersion
	if version == "" {
		version = "0.1.0"
	}
	writeJSON(response, http.StatusOK, healthResponse{ProjectID: server.ProjectID, ProtocolVersion: version})
}

func (server *Server) handleAPI(response http.ResponseWriter, request *http.Request) {
	if server.Service == nil || server.Authenticator == nil {
		writeError(response, http.StatusServiceUnavailable, "unavailable", "team service is unavailable")
		return
	}
	principal, err := server.Authenticator.Authenticate(request.Context(), request)
	if err != nil {
		writeError(response, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
		return
	}

	path := strings.TrimPrefix(request.URL.Path, teamAPIPath)
	switch {
	case request.Method == http.MethodGet && path == "status":
		status, err := server.Service.Status(request.Context(), principal)
		if err != nil {
			server.writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, status)
	case request.Method == http.MethodGet && path == "events":
		after, err := parseAfter(request)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_request", "after must be an unsigned team sequence")
			return
		}
		events, err := server.Service.Pull(request.Context(), after)
		if err != nil {
			server.writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, eventsResponse{Events: events})
	case request.Method == http.MethodPost && path == "batches":
		var batch team.PushBatch
		if !decodeRequestJSON(response, request, &batch) {
			return
		}
		result, err := server.Service.Push(request.Context(), principal, batch)
		server.writePushResult(response, result, err)
	case request.Method == http.MethodPost && path == "leases":
		var lease model.Lease
		if !decodeRequestJSON(response, request, &lease) {
			return
		}
		result, err := server.Service.IssueLease(request.Context(), principal, lease)
		server.writePushResult(response, result, err)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "leases/"):
		server.handleLeaseAction(response, request, principal, strings.TrimPrefix(path, "leases/"))
	case request.Method == http.MethodPost && path == "goal-changes":
		var change model.GoalChange
		if !decodeRequestJSON(response, request, &change) {
			return
		}
		result, err := server.Service.ProposeGoalChange(request.Context(), principal, change)
		server.writePushResult(response, result, err)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "goal-changes/"):
		server.handleGoalChangeAction(response, request, principal, strings.TrimPrefix(path, "goal-changes/"))
	case request.Method == http.MethodGet && path == "conflicts":
		status, err := server.Service.Status(request.Context(), principal)
		if err != nil {
			server.writeServiceError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, status.OpenConflicts)
	case request.Method == http.MethodPost && strings.HasPrefix(path, "conflicts/"):
		server.handleConflictAction(response, request, principal, strings.TrimPrefix(path, "conflicts/"))
	default:
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
	}
}

func (server *Server) handleLeaseAction(response http.ResponseWriter, request *http.Request, principal team.Principal, path string) {
	id, action, ok := pathAction(path)
	if !ok {
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	switch action {
	case "renew":
		var input renewLeaseRequest
		if !decodeRequestJSON(response, request, &input) {
			return
		}
		result, err := server.Service.RenewLease(request.Context(), principal, id, input.ExpiresAt)
		server.writePushResult(response, result, err)
	case "release":
		if !decodeEmptyRequest(response, request) {
			return
		}
		result, err := server.Service.ReleaseLease(request.Context(), principal, id)
		server.writePushResult(response, result, err)
	case "revoke":
		if !decodeEmptyRequest(response, request) {
			return
		}
		result, err := server.Service.RevokeLease(request.Context(), principal, id)
		server.writePushResult(response, result, err)
	default:
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
	}
}

func (server *Server) handleGoalChangeAction(response http.ResponseWriter, request *http.Request, principal team.Principal, path string) {
	id, action, ok := pathAction(path)
	if !ok {
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	switch action {
	case "approve":
		if !decodeEmptyRequest(response, request) {
			return
		}
		result, err := server.Service.ApproveGoalChange(request.Context(), principal, id)
		server.writePushResult(response, result, err)
	case "reject":
		var input rejectGoalChangeRequest
		if !decodeRequestJSON(response, request, &input) {
			return
		}
		result, err := server.Service.RejectGoalChange(request.Context(), principal, id, input.Reason)
		server.writePushResult(response, result, err)
	default:
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
	}
}

func (server *Server) handleConflictAction(response http.ResponseWriter, request *http.Request, principal team.Principal, path string) {
	id, action, ok := pathAction(path)
	if !ok || action != "resolve" {
		writeError(response, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	var input resolveConflictRequest
	if !decodeRequestJSON(response, request, &input) {
		return
	}
	result, err := server.Service.ResolveConflict(request.Context(), principal, team.ConflictResolutionRequest{ConflictID: id, Action: input.Action, Replacement: input.Replacement, Confirmed: input.Confirmed})
	server.writePushResult(response, result, err)
}

func (server *Server) writePushResult(response http.ResponseWriter, result team.PushResult, err error) {
	if err != nil {
		server.writeServiceError(response, err)
		return
	}
	switch result.Status {
	case team.PushAccepted:
		if !result.Materialized {
			writeJSON(response, http.StatusAccepted, result)
			return
		}
		writeJSON(response, http.StatusOK, result)
	case team.PushConflict:
		writeJSON(response, http.StatusConflict, result)
	case team.PushRejected:
		if result.Code == "unauthorized" {
			writeJSON(response, http.StatusForbidden, result)
			return
		}
		writeJSON(response, http.StatusUnprocessableEntity, result)
	default:
		writeError(response, http.StatusInternalServerError, "operation_failed", "team service returned an invalid result")
	}
}

func (server *Server) writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, team.ErrUnauthorized):
		writeError(response, http.StatusForbidden, "forbidden", "action is not permitted")
	case errors.Is(err, team.ErrInvalidBatch):
		writeError(response, http.StatusUnprocessableEntity, "invalid_candidate", "candidate is invalid")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(response, http.StatusServiceUnavailable, "unavailable", "team service is unavailable")
	default:
		writeError(response, http.StatusInternalServerError, "operation_failed", "team operation failed")
	}
}

func decodeRequestJSON(response http.ResponseWriter, request *http.Request, output any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid request")
		return false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "invalid_request", "invalid request")
		return false
	}
	return true
}

func decodeEmptyRequest(response http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil {
		return true
	}
	data, err := io.ReadAll(http.MaxBytesReader(response, request.Body, 1<<20))
	if err != nil || len(strings.TrimSpace(string(data))) != 0 {
		writeError(response, http.StatusBadRequest, "invalid_request", "request body must be empty")
		return false
	}
	return true
}

func parseAfter(request *http.Request) (uint64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func pathAction(path string) (string, string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func listenerIsLoopback(listener net.Listener) bool {
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, apiError{Code: code, Message: message})
}
func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

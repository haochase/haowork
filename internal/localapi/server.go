package localapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/githubscm"
	localindex "github.com/haochase/haowork/internal/index"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/haochase/haowork/internal/transfer"
	"github.com/haochase/haowork/internal/workbench"
)

const (
	healthPath          = "/_haowork/health"
	stopPath            = "/_haowork/stop"
	browserSessionsPath = "/_haowork/browser-sessions"
	sessionPath         = "/api/v1/session"
	projectPath         = "/api/v1/project"
	historyPath         = "/api/v1/history"
	requirementsPath    = "/api/v1/requirements"
	eventsPath          = "/api/v1/events"
	changesPath         = "/api/v1/changes"
	contextPath         = "/api/v1/context"
	teamPath            = "/api/v1/team"

	bootstrapHeader = "X-Haowork-Bootstrap"
	controlHeader   = "X-Haowork-Control"
	sessionCookie   = "haowork_session"

	bootstrapTTL = time.Minute
	sessionTTL   = 15 * time.Minute
)

// Server exposes a loopback-only HTTP view of one governed project.
type Server struct {
	Project       core.Project
	Sessions      *SessionStore
	Changes       changes.WorkspaceScanner
	Index         localindex.Store
	Team          TeamFacade
	Transfer      TransferFacade
	SkillRegistry *skillruntime.Registry

	Metadata   localcore.Metadata
	ControlKey string
	Stop       func()

	initializeOnce   sync.Once
	indexMu          sync.Mutex
	refreshMu        sync.Mutex
	indexOwned       bool
	closed           bool
	subscribersMu    sync.Mutex
	subscribers      map[chan sseMessage]struct{}
	transferMu       sync.Mutex
	transferPreviews map[string]transfer.ImportPreview
}

// TeamFacade keeps Team transport credentials and implementation details out
// of browser-facing handlers.
type TeamFacade interface {
	Status(context.Context) (team.Status, error)
	SyncNow(context.Context) (teamsync.SyncReport, error)
	Queue(context.Context) ([]teamsync.OutboxEntry, error)
	Conflicts(context.Context) ([]model.Conflict, error)
	ResolveConflict(context.Context, string, string) (team.PushResult, error)
	ResolveConflictRequest(context.Context, team.ConflictResolutionRequest) (team.PushResult, error)
}

type SessionStore struct {
	mu         sync.Mutex
	now        func() time.Time
	bootstraps map[string]time.Time
	sessions   map[string]time.Time
}

type PlanResponse struct {
	Requirement model.Requirement `json:"requirement"`
	Tasks       []model.Task      `json:"tasks"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type browserSessionResponse struct {
	BootstrapToken string `json:"bootstrap_token"`
}

type healthResponse struct {
	ProjectID       string `json:"project_id"`
	ProtocolVersion string `json:"protocol_version"`
	PID             int    `json:"pid"`
}

type projectProjection struct {
	model.ProjectState
	WorkspaceDigest string `json:"workspace_digest"`
}

type stateSnapshot struct {
	EventCount      int               `json:"event_count"`
	ProjectID       string            `json:"project_id"`
	WorkspaceDigest string            `json:"workspace_digest"`
	State           projectProjection `json:"state"`
}

type sseMessage struct {
	Name string
	Data stateSnapshot
}

type actorPayload struct {
	Actor model.Actor `json:"actor"`
}

type startRunPayload struct {
	Executor  string      `json:"executor"`
	ContextID string      `json:"context_id"`
	Actor     model.Actor `json:"actor"`
}

type finishRunPayload struct {
	Result string      `json:"result"`
	Actor  model.Actor `json:"actor"`
}

type verifyPayload struct {
	Kind    string      `json:"kind"`
	URI     string      `json:"uri"`
	SHA256  string      `json:"sha256"`
	Outcome string      `json:"outcome"`
	Actor   model.Actor `json:"actor"`
}

type evidenceCandidatePayload struct {
	RunID     string      `json:"run_id"`
	ContextID string      `json:"context_id"`
	Kind      string      `json:"kind"`
	URI       string      `json:"uri"`
	SHA256    string      `json:"sha256"`
	Command   string      `json:"command"`
	Outcome   string      `json:"outcome"`
	Actor     model.Actor `json:"actor"`
}

type contextBuildPayload struct {
	SupersedesID string      `json:"supersedes_id"`
	Reason       string      `json:"reason"`
	Sources      []string    `json:"sources"`
	AllowedPaths []string    `json:"allowed_paths"`
	DeniedPaths  []string    `json:"denied_paths"`
	Actor        model.Actor `json:"actor"`
}

type changeAttributePayload struct {
	SHA256 string      `json:"sha256"`
	TaskID string      `json:"task_id"`
	Note   string      `json:"note"`
	Actor  model.Actor `json:"actor"`
}

type changesResponse struct {
	Changes []model.FileChange `json:"changes"`
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		now:        time.Now,
		bootstraps: make(map[string]time.Time),
		sessions:   make(map[string]time.Time),
	}
}

func (s *Server) Handler() http.Handler {
	s.initialize()
	staticHandler := workbench.StaticHandler()
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc(sessionPath, s.handleSession)
	mux.HandleFunc(browserSessionsPath, s.handleBrowserSession)
	mux.HandleFunc(stopPath, s.handleStop)
	mux.HandleFunc(projectPath, s.handleProject)
	mux.HandleFunc(historyPath, s.handleHistory)
	mux.HandleFunc(requirementsPath, s.handleRequirements)
	mux.HandleFunc(requirementsPath+"/", s.handleRequirementAction)
	mux.HandleFunc("/api/v1/tasks/", s.handleTaskAction)
	mux.HandleFunc("/api/v1/evidence/", s.handleEvidenceAction)
	mux.HandleFunc(contextPath+"/", s.handleContext)
	mux.HandleFunc("/api/v1/runs/", s.handleRunAction)
	mux.HandleFunc(eventsPath, s.handleEvents)
	mux.HandleFunc(changesPath+"/", s.handleChanges)
	mux.HandleFunc(teamPath+"/status", s.handleTeamStatus)
	mux.HandleFunc(teamPath+"/sync", s.handleTeamSync)
	mux.HandleFunc(teamPath+"/leases", s.handleTeamLeases)
	mux.HandleFunc(teamPath+"/queue", s.handleTeamQueue)
	mux.HandleFunc(teamPath+"/conflicts", s.handleTeamConflicts)
	mux.HandleFunc(teamPath+"/conflicts/", s.handleTeamConflictAction)
	s.registerAgentTeamsRoutes(mux)
	s.registerSCMRoutes(mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isClosed() {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "local server is closed")
			return
		}
		if isLocalAPIPath(r.URL.Path) && r.URL.Path != healthPath && r.URL.Path != sessionPath && !s.authorized(r) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired session")
			return
		}
		handler, pattern := mux.Handler(r)
		if pattern == "" {
			if isLocalAPIPath(r.URL.Path) {
				writeError(w, http.StatusNotFound, "not_found", "route is not found")
				return
			}
			staticHandler.ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func (s *Server) teamUnavailable(w http.ResponseWriter) bool {
	if s.Team != nil {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, "team_unavailable", "Team collaboration is not configured")
	return true
}

func (s *Server) handleTeamStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	status, err := s.Team.Status(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleTeamSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	report, err := s.Team.SyncNow(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleTeamLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	status, err := s.Team.Status(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Leases any `json:"leases"`
	}{Leases: status.ActiveLeases})
}

func (s *Server) handleTeamQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	entries, err := s.Team.Queue(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Queue any `json:"queue"`
	}{Queue: entries})
}

func (s *Server) handleTeamConflicts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	conflicts, err := s.Team.Conflicts(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Conflicts any `json:"conflicts"`
	}{Conflicts: conflicts})
}

func (s *Server) handleTeamConflictAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/resolve") {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.teamUnavailable(w) {
		return
	}
	conflictID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, teamPath+"/conflicts/"), "/resolve")
	if strings.TrimSpace(conflictID) == "" || strings.Contains(conflictID, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "conflict id is required")
		return
	}
	var request struct {
		Action      string        `json:"action"`
		Replacement []model.Event `json:"replacement,omitempty"`
		Confirmed   bool          `json:"confirmed"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Action != team.AcceptTeam && request.Action != team.KeepAsProposal && request.Action != team.ManualMerge && request.Action != team.WithdrawLocal {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid conflict action")
		return
	}
	result, err := s.Team.ResolveConflictRequest(r.Context(), team.ConflictResolutionRequest{ConflictID: conflictID, Action: request.Action, Replacement: request.Replacement, Confirmed: request.Confirmed})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeTeamPushResult(w, result)
}

func writeTeamPushResult(w http.ResponseWriter, result team.PushResult) {
	switch result.Status {
	case team.PushAccepted:
		if !result.Materialized {
			writeJSON(w, http.StatusAccepted, result)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case team.PushConflict:
		writeJSON(w, http.StatusConflict, result)
	case team.PushRejected:
		if result.Code == "unauthorized" {
			writeJSON(w, http.StatusForbidden, result)
			return
		}
		writeJSON(w, http.StatusUnprocessableEntity, result)
	default:
		writeError(w, http.StatusInternalServerError, "operation_failed", "Team service returned an invalid result")
	}
}

// Serve runs a Local Core lifecycle and releases owned derived state on every exit path.
func (s *Server) Serve(run func(http.Handler) error) (err error) {
	defer func() {
		closeErr := s.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return run(s.Handler())
}

func (s *Server) NewBootstrapToken() string {
	if s.isClosed() {
		return ""
	}
	s.initialize()
	return s.Sessions.newBootstrapToken()
}

func (s *Server) initialize() {
	s.initializeOnce.Do(func() {
		if s.Sessions == nil {
			s.Sessions = NewSessionStore()
		}
		s.subscribers = make(map[chan sseMessage]struct{})
	})
}

func (s *SessionStore) newBootstrapToken() string {
	token, err := randomToken()
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clockNow()
	s.removeExpired(now)
	s.bootstraps[token] = now.Add(bootstrapTTL)
	return token
}

func (s *SessionStore) exchangeBootstrap(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clockNow()
	s.removeExpired(now)
	expiresAt, found := s.bootstraps[token]
	if !found || !expiresAt.After(now) {
		delete(s.bootstraps, token)
		return "", false
	}
	delete(s.bootstraps, token)
	session, err := randomToken()
	if err != nil {
		return "", false
	}
	s.sessions[session] = now.Add(sessionTTL)
	return session, true
}

func (s *SessionStore) validSession(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clockNow()
	s.removeExpired(now)
	expiresAt, found := s.sessions[token]
	if !found || !expiresAt.After(now) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *SessionStore) clockNow() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func (s *SessionStore) removeExpired(now time.Time) {
	for token, expiresAt := range s.bootstraps {
		if !expiresAt.After(now) {
			delete(s.bootstraps, token)
		}
	}
	for token, expiresAt := range s.sessions {
		if !expiresAt.After(now) {
			delete(s.sessions, token)
		}
	}
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func isLocalAPIPath(requestPath string) bool {
	return requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") ||
		requestPath == "/_haowork" || strings.HasPrefix(requestPath, "/_haowork/")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	projectID := s.Metadata.ProjectID
	if projectID == "" {
		projectID = s.Project.Manifest.ProjectID
	}
	pid := s.Metadata.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	writeJSON(w, http.StatusOK, healthResponse{ProjectID: projectID, ProtocolVersion: localcore.HealthProtocolVersion, PID: pid})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	token, ok := singleHeader(r, bootstrapHeader)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired bootstrap token")
		return
	}
	session, ok := s.Sessions.exchangeBootstrap(token)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired bootstrap token")
		return
	}
	now := s.Sessions.clockNow()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    session,
		Path:     "/",
		Expires:  now.Add(sessionTTL),
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !s.controlAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid control credential")
		return
	}
	token := s.NewBootstrapToken()
	if token == "" {
		writeError(w, http.StatusInternalServerError, "operation_failed", "local operation failed")
		return
	}
	writeJSON(w, http.StatusCreated, browserSessionResponse{BootstrapToken: token})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !s.controlAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid control credential")
		return
	}
	defer func() { _ = s.Close() }()
	if s.Stop != nil {
		s.Stop()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	state, err := s.projectProjection(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	aggregateID := r.URL.Query().Get("aggregate_id")
	if err := s.refreshIndex(r.Context()); err == nil {
		if indexStore := s.indexStore(); indexStore != nil {
			indexedEvents, indexErr := indexStore.SearchHistory(r.Context(), aggregateID, 0)
			if indexErr == nil {
				writeJSON(w, http.StatusOK, struct {
					Events []model.Event `json:"events"`
				}{Events: indexedEvents})
				return
			}
		}
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	events, err := service.History(r.Context(), aggregateID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Events []model.Event `json:"events"`
	}{Events: events})
}

func (s *Server) handleRequirements(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	var input app.PlanInput
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	requirement, tasks, err := service.Plan(r.Context(), input)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, PlanResponse{Requirement: requirement, Tasks: tasks})
}

func (s *Server) handleRequirementAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := pathAction(r.URL.Path, requirementsPath)
	if !ok || action != "approve" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	var input actorPayload
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := service.Approve(r.Context(), id, input.Actor); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTaskAction(w http.ResponseWriter, r *http.Request) {
	if id, ok := evidenceCandidateTaskID(r.URL.Path); ok {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusNotFound, "not_found", "route is not found")
			return
		}
		var input evidenceCandidatePayload
		if !decodeJSON(w, r, &input) {
			return
		}
		service, err := s.service()
		if err != nil {
			writeDomainError(w, err)
			return
		}
		record, err := service.RecordEvidenceCandidate(r.Context(), evidence.EvidenceCandidate{
			TaskID: id, RunID: input.RunID, ContextID: input.ContextID, Kind: input.Kind, URI: input.URI,
			SHA256: input.SHA256, Command: input.Command, Outcome: input.Outcome, Actor: input.Actor,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, record)
		return
	}
	id, action, ok := pathAction(r.URL.Path, "/api/v1/tasks")
	if !ok || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	switch action {
	case "context":
		var input contextBuildPayload
		if !decodeJSON(w, r, &input) {
			return
		}
		slice, err := service.BuildContext(r.Context(), app.ContextBuildInput{
			TaskID: id, SupersedesID: input.SupersedesID, Reason: input.Reason, Sources: input.Sources,
			AllowedPaths: input.AllowedPaths, DeniedPaths: input.DeniedPaths, Actor: input.Actor,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, slice)
	case "runs":
		var input startRunPayload
		if !decodeJSON(w, r, &input) {
			return
		}
		var run model.Run
		if input.ContextID == "" {
			run, err = service.StartRun(r.Context(), id, input.Executor, input.Actor)
		} else {
			run, err = service.StartRunWithContext(r.Context(), id, input.Executor, input.ContextID, input.Actor)
		}
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, run)
	case "evidence":
		var input verifyPayload
		if !decodeJSON(w, r, &input) {
			return
		}
		evidence, err := service.Verify(r.Context(), app.VerifyInput{
			TaskID: id, Kind: input.Kind, URI: input.URI, SHA256: input.SHA256, Outcome: input.Outcome, Actor: input.Actor,
		})
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, evidence)
	case "complete":
		var input actorPayload
		if !decodeJSON(w, r, &input) {
			return
		}
		if err := service.Complete(r.Context(), id, input.Actor); err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
	}
}

func (s *Server) handleEvidenceAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := pathAction(r.URL.Path, "/api/v1/evidence")
	if !ok || action != "verify" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	var input actorPayload
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	record, err := service.VerifyEvidence(r.Context(), id, input.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, record)
}

func evidenceCandidateTaskID(path string) (string, bool) {
	const prefix = "/api/v1/tasks/"
	const suffix = "/evidence/candidates"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	contextID := strings.TrimPrefix(r.URL.Path, contextPath+"/")
	if contextID == "" || strings.Contains(contextID, "/") {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	slice, err := service.GetContext(r.Context(), contextID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, slice)
}

func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := pathAction(r.URL.Path, "/api/v1/runs")
	if !ok || action != "finish" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	var input finishRunPayload
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := service.FinishRun(r.Context(), id, input.Result, input.Actor); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if r.URL.Path == changesPath+"/scan" {
		var input actorPayload
		if !decodeJSON(w, r, &input) {
			return
		}
		fileChanges, err := s.scanner().Scan(r.Context(), s.Project.Root)
		if err != nil {
			writeDomainError(w, fmt.Errorf("%w: scan workspace changes: %v", app.ErrOperational, err))
			return
		}
		service, err := s.service()
		if err != nil {
			writeDomainError(w, err)
			return
		}
		if err := service.RecordScan(r.Context(), fileChanges, input.Actor); err != nil {
			writeDomainError(w, err)
			return
		}
		if err := s.afterWrite(r.Context()); err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, changesResponse{Changes: fileChanges})
		return
	}

	path, ok := changeAttributePath(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	var input changeAttributePayload
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := service.AttributeChange(r.Context(), path, input.SHA256, input.TaskID, input.Note, input.Actor); err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func changeAttributePath(r *http.Request) (string, bool) {
	const prefix = changesPath + "/"
	const suffix = "/attribute"
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(escaped, prefix)
	suffixIndex := strings.LastIndex(rest, suffix)
	if suffixIndex <= 0 || suffixIndex+len(suffix) != len(rest) {
		return "", false
	}
	encodedPath := rest[:suffixIndex]
	if encodedPath == "" || strings.Contains(encodedPath, "/") {
		return "", false
	}
	path, err := url.PathUnescape(encodedPath)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	return path, true
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "operation_failed", "local operation failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	subscriber := make(chan sseMessage, 1)
	s.subscribersMu.Lock()
	snapshot, err := s.snapshot(r.Context())
	if err == nil {
		s.subscribers[subscriber] = struct{}{}
	}
	s.subscribersMu.Unlock()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	defer func() {
		s.subscribersMu.Lock()
		delete(s.subscribers, subscriber)
		s.subscribersMu.Unlock()
	}()

	if !writeSSE(w, flusher, sseMessage{Name: "snapshot", Data: snapshot}) {
		return
	}
	for {
		select {
		case message := <-subscriber:
			if !writeSSE(w, flusher, message) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.controlAuthorized(r) {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && s.Sessions.validSession(cookie.Value)
}

func (s *Server) controlAuthorized(r *http.Request) bool {
	key := s.ControlKey
	if key == "" {
		key = s.Metadata.ControlKey
	}
	value, ok := singleHeader(r, controlHeader)
	return ok && key != "" && subtle.ConstantTimeCompare([]byte(value), []byte(key)) == 1
}

func singleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return values[0], true
}

func (s *Server) service() (*app.Service, error) {
	if s.Project.Service == nil {
		return nil, fmt.Errorf("%w: project service is unavailable", app.ErrOperational)
	}
	if s.Project.Root != "" {
		s.Project.Service.ConfigureWorkspaceScanner(s.scanner(), s.Project.Root)
	}
	return s.Project.Service, nil
}

func (s *Server) scanner() changes.WorkspaceScanner {
	if s.Changes != nil {
		return s.Changes
	}
	return changes.Scanner{}
}

func (s *Server) indexStore() localindex.Store {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.closed {
		return nil
	}
	if s.Index != nil || s.Project.Root == "" {
		return s.Index
	}
	store, err := localindex.Open(s.Project.Root)
	if err == nil {
		s.Index = store
		s.indexOwned = true
	}
	return s.Index
}

func (s *Server) Close() error {
	s.indexMu.Lock()
	if s.closed {
		s.indexMu.Unlock()
		return nil
	}
	s.closed = true
	store := s.Index
	owned := s.indexOwned
	if owned {
		s.Index = nil
		s.indexOwned = false
	}
	s.indexMu.Unlock()
	if !owned || store == nil {
		return nil
	}
	return store.Close()
}

func (s *Server) isClosed() bool {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	return s.closed
}

func (s *Server) refreshIndex(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	store := s.indexStore()
	if store == nil {
		return nil
	}
	events, err := s.Project.Events.ReadAll(ctx)
	if err != nil {
		return fmt.Errorf("read event history for local index: %w", err)
	}
	want, err := localindex.WatermarkForEvents(events)
	if err != nil {
		return err
	}
	got, err := store.Watermark(ctx)
	if err == nil && got.Equal(want) {
		return nil
	}
	if err := store.Rebuild(ctx, events); err != nil {
		return fmt.Errorf("rebuild local index: %w", err)
	}
	got, err = store.Watermark(ctx)
	if err != nil {
		return fmt.Errorf("read rebuilt local index watermark: %w", err)
	}
	if !got.Equal(want) {
		return fmt.Errorf("rebuilt local index watermark = %#v, want %#v", got, want)
	}
	return nil
}

func (s *Server) afterWrite(ctx context.Context) error {
	// The event append is already durable; a cache failure must not misreport it as a failed write.
	if err := s.refreshIndex(ctx); err != nil {
		// History falls back to JSONL if the local index cannot answer the query.
	}
	return s.broadcastCurrentState(ctx)
}

func (s *Server) status(ctx context.Context) (model.ProjectState, error) {
	service, err := s.service()
	if err != nil {
		return model.ProjectState{}, err
	}
	return service.Status(ctx)
}

func (s *Server) snapshot(ctx context.Context) (stateSnapshot, error) {
	if s.Project.Service == nil {
		return stateSnapshot{ProjectID: s.projectID()}, nil
	}
	state, err := s.projectProjection(ctx)
	if err != nil {
		return stateSnapshot{}, err
	}
	snapshot := stateSnapshot{ProjectID: state.ProjectID, WorkspaceDigest: state.WorkspaceDigest, State: state}
	if s.Project.Root == "" {
		return snapshot, nil
	}
	events, err := s.Project.Events.ReadAll(ctx)
	if err != nil {
		return stateSnapshot{}, fmt.Errorf("%w: read event count: %v", app.ErrOperational, err)
	}
	snapshot.EventCount = len(events)
	return snapshot, nil
}

func (s *Server) projectProjection(ctx context.Context) (projectProjection, error) {
	state, err := s.status(ctx)
	if err != nil {
		return projectProjection{}, err
	}
	projection := projectProjection{ProjectState: state}
	if s.Project.Root == "" {
		return projection, nil
	}
	changes, err := s.scanner().Scan(ctx, s.Project.Root)
	if err != nil {
		return projection, nil
	}
	digest, err := evidence.WorkspaceDigest(changes)
	if err != nil {
		return projection, nil
	}
	projection.WorkspaceDigest = digest
	return projection, nil
}

func (s *Server) projectID() string {
	if s.Metadata.ProjectID != "" {
		return s.Metadata.ProjectID
	}
	return s.Project.Manifest.ProjectID
}

func (s *Server) broadcastCurrentState(ctx context.Context) error {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	s.subscribersMu.Lock()
	defer s.subscribersMu.Unlock()
	for subscriber := range s.subscribers {
		select {
		case subscriber <- sseMessage{Name: "state.changed", Data: snapshot}:
		default:
		}
	}
	return nil
}

func pathAction(target, prefix string) (string, string, bool) {
	path := strings.TrimPrefix(target, prefix+"/")
	if path == target {
		return "", "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return false
	}
	return true
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, message sseMessage) bool {
	data, err := json.Marshal(message.Data)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", message.Name, data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func writeDomainError(w http.ResponseWriter, err error) {
	var apiErr *teamapi.APIError
	var githubErr *githubscm.APIError
	var conflictErr *teamapi.ConflictError
	switch {
	case errors.Is(err, teamsync.ErrOffline), errors.Is(err, team.ErrNotWritable):
		writeError(w, http.StatusServiceUnavailable, "team_offline", "Team Core is unavailable")
	case errors.As(err, &apiErr):
		status := apiErr.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		writeError(w, status, apiErr.Code, apiErr.Message)
	case errors.As(err, &githubErr):
		status := githubErr.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		writeError(w, status, githubErr.Code, "GitHub dependency unavailable")
	case errors.As(err, &conflictErr), errors.Is(err, teamsync.ErrStaleCursor):
		writeError(w, http.StatusConflict, "conflict", "Team history requires explicit reconciliation")
	case errors.Is(err, app.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "repository changed since status check")
	case errors.Is(err, app.ErrApprovalRequired):
		writeError(w, http.StatusForbidden, "forbidden", "action is not permitted")
	case errors.Is(err, app.ErrGateFailed):
		writeError(w, http.StatusUnprocessableEntity, "gate_failed", "action cannot pass current gates")
	case errors.Is(err, app.ErrOperational):
		writeError(w, http.StatusInternalServerError, "operation_failed", "local operation failed")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "local core is unavailable")
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

package localapi

import (
	"net/http"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
)

const scmPath = "/api/v1/scm"

type SCMStatusResponse struct {
	Repositories []model.SCMRepository `json:"repositories"`
	Commits      []SCMCommitResponse   `json:"commits"`
	Bindings     []model.SCMBinding    `json:"bindings"`
}

type SCMCommitResponse struct {
	Observation model.CommitObservation `json:"observation"`
	Status      string                  `json:"status"`
}

type scmObserveRequest struct {
	RepositoryID string      `json:"repository_id"`
	CommitOID    string      `json:"commit_oid"`
	Actor        model.Actor `json:"actor"`
}

type scmBindingRequest struct {
	RepositoryID string      `json:"repository_id"`
	CommitOID    string      `json:"commit_oid"`
	TaskIDs      []string    `json:"task_ids"`
	MissionID    string      `json:"mission_id"`
	EvidenceIDs  []string    `json:"evidence_ids"`
	TraceIDs     []string    `json:"trace_ids"`
	Actor        model.Actor `json:"actor"`
}

type scmBindingDecisionRequest struct {
	Reason string      `json:"reason,omitempty"`
	Actor  model.Actor `json:"actor"`
}

type scmHistoryRequest struct {
	RepositoryID string      `json:"repository_id"`
	Refs         []string    `json:"refs"`
	Actor        model.Actor `json:"actor"`
}

func (s *Server) registerSCMRoutes(mux *http.ServeMux) {
	mux.HandleFunc(scmPath+"/status", s.handleSCMStatus)
	mux.HandleFunc(scmPath+"/register", s.handleSCMRegister)
	mux.HandleFunc(scmPath+"/commits/observe", s.handleSCMObserve)
	mux.HandleFunc(scmPath+"/commits/", s.handleSCMCommit)
	mux.HandleFunc(scmPath+"/bindings", s.handleSCMBindings)
	mux.HandleFunc(scmPath+"/bindings/", s.handleSCMBindingAction)
	mux.HandleFunc(scmPath+"/history/verify", s.handleSCMHistory)
	s.registerGitHubSCMRoutes(mux)
}

func (s *Server) scmUnavailable(w http.ResponseWriter) bool {
	if s.Project.SCMAvailable {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, "scm_unavailable", "local Git inspection is not configured")
	return true
}

func (s *Server) handleSCMStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	state, err := service.Status(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scmStatusProjection(state))
}

func (s *Server) handleSCMRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	var request actorPayload
	if !decodeJSON(w, r, &request) {
		return
	}
	repository, err := s.Project.Service.RegisterSCM(r.Context(), request.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repository)
}

func (s *Server) handleSCMObserve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	var request scmObserveRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	observation, err := s.Project.Service.ObserveSCMCommit(r.Context(), request.RepositoryID, request.CommitOID, request.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, observation)
}

func (s *Server) handleSCMCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	oid := strings.TrimPrefix(r.URL.Path, scmPath+"/commits/")
	if oid == "" || strings.Contains(oid, "/") {
		writeError(w, http.StatusNotFound, "not_found", "SCM commit is not found")
		return
	}
	state, err := s.Project.Service.Status(r.Context())
	if err != nil {
		writeDomainError(w, err)
		return
	}
	var match *SCMCommitResponse
	for key, observation := range state.CommitObservations {
		if observation.CommitOID != oid {
			continue
		}
		if match != nil {
			writeError(w, http.StatusConflict, "conflict", "commit OID exists in multiple registered repositories")
			return
		}
		value := SCMCommitResponse{Observation: observation, Status: state.SCMCommitStatus[key]}
		match = &value
	}
	if match == nil {
		writeError(w, http.StatusNotFound, "not_found", "SCM commit is not found")
		return
	}
	writeJSON(w, http.StatusOK, match)
}

func (s *Server) handleSCMBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	var request scmBindingRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	binding, err := s.Project.Service.ProposeSCMBinding(r.Context(), app.ProposeSCMBindingInput{
		RepositoryID: request.RepositoryID, CommitOID: request.CommitOID, TaskIDs: request.TaskIDs,
		MissionID: request.MissionID, EvidenceIDs: request.EvidenceIDs, TraceIDs: request.TraceIDs, Actor: request.Actor,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, binding)
}

func (s *Server) handleSCMBindingAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, scmPath+"/bindings/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not_found", "SCM binding action is not found")
		return
	}
	var request scmBindingDecisionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	var (
		binding model.SCMBinding
		err     error
	)
	switch parts[1] {
	case "confirm":
		binding, err = s.Project.Service.ConfirmSCMBinding(r.Context(), parts[0], request.Actor)
	case "reject":
		binding, err = s.Project.Service.RejectSCMBinding(r.Context(), parts[0], request.Reason, request.Actor)
	default:
		writeError(w, http.StatusNotFound, "not_found", "SCM binding action is not found")
		return
	}
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, binding)
}

func (s *Server) handleSCMHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.scmUnavailable(w) {
		return
	}
	var request scmHistoryRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	report, err := s.Project.Service.VerifySCMHistory(r.Context(), request.RepositoryID, request.Refs, request.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func scmStatusProjection(state model.ProjectState) SCMStatusResponse {
	response := SCMStatusResponse{
		Repositories: make([]model.SCMRepository, 0, len(state.SCMRepositories)),
		Commits:      make([]SCMCommitResponse, 0, len(state.CommitObservations)),
		Bindings:     make([]model.SCMBinding, 0, len(state.SCMBindings)),
	}
	for _, repository := range state.SCMRepositories {
		response.Repositories = append(response.Repositories, repository)
	}
	for key, observation := range state.CommitObservations {
		response.Commits = append(response.Commits, SCMCommitResponse{Observation: observation, Status: state.SCMCommitStatus[key]})
	}
	for _, binding := range state.SCMBindings {
		response.Bindings = append(response.Bindings, binding)
	}
	sort.Slice(response.Repositories, func(i, j int) bool { return response.Repositories[i].ID < response.Repositories[j].ID })
	sort.Slice(response.Commits, func(i, j int) bool {
		if response.Commits[i].Observation.CommittedAt.Equal(response.Commits[j].Observation.CommittedAt) {
			return response.Commits[i].Observation.CommitOID < response.Commits[j].Observation.CommitOID
		}
		return response.Commits[i].Observation.CommittedAt.Before(response.Commits[j].Observation.CommittedAt)
	})
	sort.Slice(response.Bindings, func(i, j int) bool { return response.Bindings[i].ID < response.Bindings[j].ID })
	return response
}

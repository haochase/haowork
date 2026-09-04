package localapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/trace"
	"github.com/haochase/haowork/internal/transfer"
)

// TransferFacade keeps signing keys and import writers outside the browser API.
// Implementations must perform their own provenance and approval checks.
type TransferFacade interface {
	Export(context.Context, json.RawMessage) ([]byte, error)
	BuildReturn(context.Context, json.RawMessage) (transfer.ReturnDelta, error)
	Preview(context.Context, []byte) (transfer.ImportPreview, error)
	Apply(context.Context, transfer.ImportPreview, model.Actor) error
}

type projectTransferFacade struct{ service *transfer.Service }

func (facade projectTransferFacade) Export(_ context.Context, payload json.RawMessage) ([]byte, error) {
	if facade.service == nil || facade.service.ReturnSigner == nil || facade.service.ProvenanceVerifier == nil {
		return nil, errors.New("transfer export requires Core-owned signed request")
	}
	var input struct {
		Manifest transfer.Manifest `json:"manifest"`
		Entries  []transfer.Entry  `json:"entries"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return nil, errors.New("invalid signed transfer request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid signed transfer request")
	}
	return transfer.ExportBytes(transfer.ExportInput{Manifest: input.Manifest, Entries: input.Entries, Signer: facade.service.ReturnSigner, ProvenanceVerifier: facade.service.ProvenanceVerifier})
}

func (facade projectTransferFacade) BuildReturn(ctx context.Context, payload json.RawMessage) (transfer.ReturnDelta, error) {
	if facade.service == nil {
		return transfer.ReturnDelta{}, errors.New("transfer return requires Core-owned service")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request transfer.ReturnRequest
	if err := decoder.Decode(&request); err != nil {
		return transfer.ReturnDelta{}, errors.New("invalid transfer return request")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return transfer.ReturnDelta{}, errors.New("invalid transfer return request")
	}
	return facade.service.BuildReturn(ctx, request)
}
func (facade projectTransferFacade) Preview(ctx context.Context, archive []byte) (transfer.ImportPreview, error) {
	return facade.service.PreviewImport(ctx, archive)
}
func (facade projectTransferFacade) Apply(ctx context.Context, preview transfer.ImportPreview, actor model.Actor) error {
	return facade.service.ApplyImport(ctx, preview, transfer.Approval{PreviewHash: preview.PreviewHash, Actor: actor})
}

type MissionRequest struct {
	MissionID          string                         `json:"mission_id,omitempty"`
	TaskIDs            []string                       `json:"task_ids"`
	CompletionCriteria []string                       `json:"completion_criteria"`
	AllowedScopes      []string                       `json:"allowed_scopes"`
	Skills             []model.MissionSkillGrant      `json:"skills"`
	Assignments        map[model.AgentFunction]string `json:"assignments"`
	ContextID          string                         `json:"context_id"`
	RiskLevel          string                         `json:"risk_level"`
	EnvironmentID      string                         `json:"environment_id"`
	PolicyVersion      string                         `json:"policy_version"`
	IssuedAt           string                         `json:"issued_at,omitempty"`
	Deadline           string                         `json:"deadline,omitempty"`
	Actor              model.Actor                    `json:"actor"`
}

type MissionResponse struct {
	Missions []model.MissionEnvelope `json:"missions"`
}
type AgentTopologyResponse struct {
	Agents   []model.LogicalAgent              `json:"agents"`
	Bindings map[string][]model.RuntimeBinding `json:"bindings"`
}
type SkillResponse struct {
	Skills []skillruntime.Definition `json:"skills"`
}
type TraceResponse struct {
	Traces []trace.Envelope `json:"traces"`
	Next   string           `json:"next,omitempty"`
}
type ApprovalResponse struct {
	Approvals []model.ApprovalRequest `json:"approvals"`
}

type approvalDecisionRequest struct {
	PayloadSHA256 string      `json:"payload_sha256"`
	Decision      string      `json:"decision"`
	Reason        string      `json:"reason"`
	Actor         model.Actor `json:"actor"`
}
type transferArchiveRequest struct {
	Archive   string      `json:"archive"`
	Actor     model.Actor `json:"actor,omitempty"`
	Confirmed bool        `json:"confirmed,omitempty"`
}
type transferPreviewResponse struct {
	PreviewHash    string                     `json:"preview_hash"`
	Manifest       transfer.Manifest          `json:"manifest"`
	RebindRequired []transfer.RebindCandidate `json:"rebind_required"`
}
type transferApplyRequest struct {
	PreviewHash string      `json:"preview_hash"`
	Actor       model.Actor `json:"actor"`
	Confirmed   bool        `json:"confirmed"`
}
type rebindRequest struct {
	Binding   model.RuntimeBinding `json:"binding"`
	Actor     model.Actor          `json:"actor"`
	Confirmed bool                 `json:"confirmed"`
}

func (s *Server) registerAgentTeamsRoutes(mux *http.ServeMux) {
	if s.Transfer == nil && s.Project.Transfer != nil {
		s.Transfer = projectTransferFacade{service: s.Project.Transfer}
	}
	mux.HandleFunc("/api/v1/missions", s.handleMissions)
	mux.HandleFunc("/api/v1/agentteams/topology", s.handleTopology)
	mux.HandleFunc("/api/v1/skills", s.handleSkills)
	mux.HandleFunc("/api/v1/traces", s.handleTraces)
	mux.HandleFunc("/api/v1/approvals", s.handleApprovals)
	mux.HandleFunc("/api/v1/approvals/", s.handleApprovalAction)
	mux.HandleFunc("/api/v1/transfers/export", s.handleTransferExport)
	mux.HandleFunc("/api/v1/transfers/return", s.handleTransferReturn)
	mux.HandleFunc("/api/v1/transfers/preview", s.handleTransferPreview)
	mux.HandleFunc("/api/v1/transfers/", s.handleTransferApply)
	mux.HandleFunc("/api/v1/agents/", s.handleAgentAction)
}

func (s *Server) handleMissions(w http.ResponseWriter, r *http.Request) {
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
	if r.Method == http.MethodGet {
		values := sortedMissions(state.Missions)
		writeJSON(w, http.StatusOK, MissionResponse{Missions: values})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	var input MissionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	issue, err := input.issueInput()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid mission request")
		return
	}
	mission, err := service.IssueMission(r.Context(), issue)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mission)
}

func (input MissionRequest) issueInput() (app.IssueMissionInput, error) {
	result := app.IssueMissionInput{MissionID: input.MissionID, TaskIDs: input.TaskIDs, CompletionCriteria: input.CompletionCriteria, AllowedScopes: input.AllowedScopes, Skills: input.Skills, Assignments: input.Assignments, ContextID: input.ContextID, RiskLevel: input.RiskLevel, EnvironmentID: input.EnvironmentID, PolicyVersion: input.PolicyVersion, Actor: input.Actor}
	if input.IssuedAt != "" {
		parsed, err := timeParse(input.IssuedAt)
		if err != nil {
			return result, err
		}
		result.IssuedAt = parsed
	}
	if input.Deadline != "" {
		parsed, err := timeParse(input.Deadline)
		if err != nil {
			return result, err
		}
		result.Deadline = parsed
	}
	return result, nil
}

func timeParse(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
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
	writeJSON(w, http.StatusOK, AgentTopologyResponse{Agents: sortedAgents(state.Agents), Bindings: state.RuntimeBindings})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	registry := s.SkillRegistry
	if registry == nil && s.Project.Root != "" {
		loaded, err := skillruntime.Load(s.Project.Root + "/skills")
		if err == nil {
			registry = loaded
		}
	}
	if registry == nil {
		writeJSON(w, http.StatusOK, SkillResponse{Skills: []skillruntime.Definition{}})
		return
	}
	writeJSON(w, http.StatusOK, SkillResponse{Skills: registry.Definitions()})
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.Project.Root == "" {
		writeJSON(w, http.StatusOK, TraceResponse{Traces: []trace.Envelope{}})
		return
	}
	store := trace.New(s.Project.Root)
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") != "" && err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "after must be an unsigned sequence")
		return
	}
	values, err := store.Since(r.Context(), after)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	missionID := r.URL.Query().Get("mission_id")
	filtered := values[:0]
	for _, value := range values {
		if missionID == "" || value.MissionID == missionID {
			filtered = append(filtered, value)
		}
	}
	values = filtered
	var next string
	if len(values) > 0 {
		next = strconv.FormatUint(values[len(values)-1].Sequence, 10)
	}
	writeJSON(w, http.StatusOK, TraceResponse{Traces: values, Next: next})
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
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
	values := sortedApprovals(state.Approvals)
	writeJSON(w, http.StatusOK, ApprovalResponse{Approvals: values})
}

func (s *Server) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/decide") {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/approvals/"), "/decide")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "approval id is required")
		return
	}
	var input approvalDecisionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	approval, err := service.DecideApproval(r.Context(), id, input.PayloadSHA256, input.Decision, input.Reason, input.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approval)
}

func (s *Server) handleTransferExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.Transfer == nil {
		writeError(w, http.StatusServiceUnavailable, "transfer_unavailable", "transfer service is unavailable")
		return
	}
	var raw json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := decoder.Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid transfer request")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid transfer request")
		return
	}
	archive, err := s.Transfer.Export(r.Context(), raw)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"archive": base64.StdEncoding.EncodeToString(archive)})
}

func (s *Server) handleTransferReturn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.Transfer == nil {
		writeError(w, http.StatusServiceUnavailable, "transfer_unavailable", "transfer service is unavailable")
		return
	}
	var raw json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := decoder.Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid transfer return request")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid transfer return request")
		return
	}
	result, err := s.Transfer.BuildReturn(r.Context(), raw)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Archive   string            `json:"archive"`
		Manifest  transfer.Manifest `json:"manifest"`
		Conflicts []string          `json:"conflicts"`
	}{Archive: base64.StdEncoding.EncodeToString(result.Archive), Manifest: result.Manifest, Conflicts: result.Conflicts})
}

func (s *Server) handleTransferPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if s.Transfer == nil {
		writeError(w, http.StatusServiceUnavailable, "transfer_unavailable", "transfer service is unavailable")
		return
	}
	var input transferArchiveRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	archive, err := base64.StdEncoding.DecodeString(input.Archive)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "archive must be base64")
		return
	}
	preview, err := s.Transfer.Preview(r.Context(), archive)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.storeTransferPreview(preview)
	writeJSON(w, http.StatusOK, transferPreviewResponse{PreviewHash: preview.PreviewHash, Manifest: preview.Manifest, RebindRequired: preview.RebindRequired})
}

func (s *Server) handleTransferApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/apply") {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	if s.Transfer == nil {
		writeError(w, http.StatusServiceUnavailable, "transfer_unavailable", "transfer service is unavailable")
		return
	}
	var input transferApplyRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirmed || input.Actor.Kind != model.ActorHuman || input.Actor.Role != model.RoleOwner {
		writeError(w, http.StatusForbidden, "approval_required", "owner confirmation is required")
		return
	}
	preview, ok := s.loadTransferPreview(input.PreviewHash)
	if !ok {
		writeError(w, http.StatusConflict, "preview_required", "matching transfer preview is required")
		return
	}
	if err := s.Transfer.Apply(r.Context(), preview, input.Actor); err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "applied", "preview_hash": input.PreviewHash})
}

func (s *Server) handleAgentAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/rebind") {
		writeError(w, http.StatusNotFound, "not_found", "route is not found")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/"), "/rebind")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "invalid_request", "agent id is required")
		return
	}
	var input rebindRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if !input.Confirmed || input.Actor.Kind != model.ActorHuman || input.Actor.Role != model.RoleOwner {
		writeError(w, http.StatusForbidden, "approval_required", "owner confirmation is required")
		return
	}
	input.Binding.LogicalActorID = id
	service, err := s.service()
	if err != nil {
		writeDomainError(w, err)
		return
	}
	values, err := service.BindRuntimeTopology(r.Context(), []model.RuntimeBinding{input.Binding}, input.Actor)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.afterWrite(r.Context()); err != nil {
		writeDomainError(w, err)
		return
	}
	if len(values) == 0 {
		writeError(w, http.StatusInternalServerError, "operation_failed", "runtime binding was not recorded")
		return
	}
	writeJSON(w, http.StatusOK, values[0])
}

func sortedAgents(values map[string]model.LogicalAgent) []model.LogicalAgent {
	result := make([]model.LogicalAgent, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedMissions(values map[string]model.MissionEnvelope) []model.MissionEnvelope {
	result := make([]model.MissionEnvelope, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedApprovals(values map[string]model.ApprovalRequest) []model.ApprovalRequest {
	result := make([]model.ApprovalRequest, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Server) storeTransferPreview(preview transfer.ImportPreview) {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	if s.transferPreviews == nil {
		s.transferPreviews = make(map[string]transfer.ImportPreview)
	}
	s.transferPreviews[preview.PreviewHash] = preview
}

func (s *Server) loadTransferPreview(hash string) (transfer.ImportPreview, bool) {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	preview, ok := s.transferPreviews[hash]
	return preview, ok
}

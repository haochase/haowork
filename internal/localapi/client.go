package localapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamsync"
)

type Client struct {
	endpoint   string
	controlKey string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("local API returned status %d", e.StatusCode)
}

func NewClient(metadata localcore.Metadata) *Client {
	return &Client{
		endpoint:   strings.TrimRight(metadata.Endpoint, "/"),
		controlKey: metadata.ControlKey,
		httpClient: &http.Client{},
	}
}

func (c *Client) Plan(ctx context.Context, input app.PlanInput) (PlanResponse, error) {
	var response PlanResponse
	if err := c.doJSON(ctx, http.MethodPost, requirementsPath, input, &response); err != nil {
		return PlanResponse{}, err
	}
	return response, nil
}

func (c *Client) Status(ctx context.Context) (model.ProjectState, error) {
	var response model.ProjectState
	if err := c.doJSON(ctx, http.MethodGet, projectPath, nil, &response); err != nil {
		return model.ProjectState{}, err
	}
	return response, nil
}

func (c *Client) SCMStatus(ctx context.Context) (SCMStatusResponse, error) {
	var response SCMStatusResponse
	if err := c.doJSON(ctx, http.MethodGet, scmPath+"/status", nil, &response); err != nil {
		return SCMStatusResponse{}, err
	}
	return response, nil
}

func (c *Client) RegisterSCM(ctx context.Context, actor model.Actor) (model.SCMRepository, error) {
	var response model.SCMRepository
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/register", actorPayload{Actor: actor}, &response); err != nil {
		return model.SCMRepository{}, err
	}
	return response, nil
}

func (c *Client) ObserveSCMCommit(ctx context.Context, repositoryID, commitOID string, actor model.Actor) (model.CommitObservation, error) {
	var response model.CommitObservation
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/commits/observe", scmObserveRequest{RepositoryID: repositoryID, CommitOID: commitOID, Actor: actor}, &response); err != nil {
		return model.CommitObservation{}, err
	}
	return response, nil
}

func (c *Client) SCMCommit(ctx context.Context, commitOID string) (SCMCommitResponse, error) {
	var response SCMCommitResponse
	if err := c.doJSON(ctx, http.MethodGet, scmPath+"/commits/"+url.PathEscape(commitOID), nil, &response); err != nil {
		return SCMCommitResponse{}, err
	}
	return response, nil
}

func (c *Client) ProposeSCMBinding(ctx context.Context, input app.ProposeSCMBindingInput) (model.SCMBinding, error) {
	var response model.SCMBinding
	request := scmBindingRequest{
		RepositoryID: input.RepositoryID, CommitOID: input.CommitOID, TaskIDs: input.TaskIDs,
		MissionID: input.MissionID, EvidenceIDs: input.EvidenceIDs, TraceIDs: input.TraceIDs, Actor: input.Actor,
	}
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/bindings", request, &response); err != nil {
		return model.SCMBinding{}, err
	}
	return response, nil
}

func (c *Client) ConfirmSCMBinding(ctx context.Context, bindingID string, actor model.Actor) (model.SCMBinding, error) {
	var response model.SCMBinding
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/bindings/"+url.PathEscape(bindingID)+"/confirm", scmBindingDecisionRequest{Actor: actor}, &response); err != nil {
		return model.SCMBinding{}, err
	}
	return response, nil
}

func (c *Client) RejectSCMBinding(ctx context.Context, bindingID, reason string, actor model.Actor) (model.SCMBinding, error) {
	var response model.SCMBinding
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/bindings/"+url.PathEscape(bindingID)+"/reject", scmBindingDecisionRequest{Reason: reason, Actor: actor}, &response); err != nil {
		return model.SCMBinding{}, err
	}
	return response, nil
}

func (c *Client) VerifySCMHistory(ctx context.Context, repositoryID string, refs []string, actor model.Actor) (app.SCMHistoryReport, error) {
	var response app.SCMHistoryReport
	if err := c.doJSON(ctx, http.MethodPost, scmPath+"/history/verify", scmHistoryRequest{RepositoryID: repositoryID, Refs: refs, Actor: actor}, &response); err != nil {
		return app.SCMHistoryReport{}, err
	}
	return response, nil
}

func (c *Client) Approve(ctx context.Context, requirementID string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, requirementsPath+"/"+url.PathEscape(requirementID)+"/approve", actorPayload{Actor: actor}, nil)
}

func (c *Client) StartRun(ctx context.Context, taskID, executor string, actor model.Actor) (model.Run, error) {
	return c.StartRunWithContext(ctx, taskID, executor, "", actor)
}

func (c *Client) StartRunWithContext(ctx context.Context, taskID, executor, contextID string, actor model.Actor) (model.Run, error) {
	var response model.Run
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/runs", startRunPayload{Executor: executor, ContextID: contextID, Actor: actor}, &response); err != nil {
		return model.Run{}, err
	}
	return response, nil
}

func (c *Client) FinishRun(ctx context.Context, runID, result string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/runs/"+url.PathEscape(runID)+"/finish", finishRunPayload{Result: result, Actor: actor}, nil)
}

func (c *Client) Verify(ctx context.Context, input app.VerifyInput) (model.Evidence, error) {
	var response model.Evidence
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(input.TaskID)+"/evidence", verifyPayload{
		Kind: input.Kind, URI: input.URI, SHA256: input.SHA256, Outcome: input.Outcome, Actor: input.Actor,
	}, &response); err != nil {
		return model.Evidence{}, err
	}
	return response, nil
}

func (c *Client) RecordEvidenceCandidate(ctx context.Context, input evidence.EvidenceCandidate) (model.Evidence, error) {
	var response model.Evidence
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(input.TaskID)+"/evidence/candidates", evidenceCandidatePayload{
		RunID: input.RunID, ContextID: input.ContextID, Kind: input.Kind, URI: input.URI, SHA256: input.SHA256,
		Command: input.Command, Outcome: input.Outcome, Actor: input.Actor,
	}, &response); err != nil {
		return model.Evidence{}, err
	}
	return response, nil
}

func (c *Client) VerifyEvidence(ctx context.Context, evidenceID string, actor model.Actor) (model.Evidence, error) {
	var response model.Evidence
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/evidence/"+url.PathEscape(evidenceID)+"/verify", actorPayload{Actor: actor}, &response); err != nil {
		return model.Evidence{}, err
	}
	return response, nil
}

func (c *Client) BuildContext(ctx context.Context, input app.ContextBuildInput) (model.ContextSlice, error) {
	var response model.ContextSlice
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(input.TaskID)+"/context", contextBuildPayload{
		SupersedesID: input.SupersedesID, Reason: input.Reason, Sources: input.Sources, AllowedPaths: input.AllowedPaths, DeniedPaths: input.DeniedPaths, Actor: input.Actor,
	}, &response); err != nil {
		return model.ContextSlice{}, err
	}
	return response, nil
}

func (c *Client) GetContext(ctx context.Context, contextID string) (model.ContextSlice, error) {
	var response model.ContextSlice
	if err := c.doJSON(ctx, http.MethodGet, contextPath+"/"+url.PathEscape(contextID), nil, &response); err != nil {
		return model.ContextSlice{}, err
	}
	return response, nil
}

func (c *Client) ScanChanges(ctx context.Context, actor model.Actor) ([]model.FileChange, error) {
	var response changesResponse
	if err := c.doJSON(ctx, http.MethodPost, changesPath+"/scan", actorPayload{Actor: actor}, &response); err != nil {
		return nil, err
	}
	return response.Changes, nil
}

func (c *Client) AttributeChange(ctx context.Context, path, sha256, taskID, note string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, changesPath+"/"+url.PathEscape(path)+"/attribute", changeAttributePayload{
		SHA256: sha256, TaskID: taskID, Note: note, Actor: actor,
	}, nil)
}

func (c *Client) Complete(ctx context.Context, taskID string, actor model.Actor) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/complete", actorPayload{Actor: actor}, nil)
}

func (c *Client) History(ctx context.Context, aggregateID string) ([]model.Event, error) {
	path := historyPath
	if aggregateID != "" {
		path += "?" + url.Values{"aggregate_id": []string{aggregateID}}.Encode()
	}
	var response struct {
		Events []model.Event `json:"events"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Events, nil
}

func (c *Client) CreateBrowserSession(ctx context.Context) (string, error) {
	var response browserSessionResponse
	if err := c.doJSON(ctx, http.MethodPost, browserSessionsPath, nil, &response); err != nil {
		return "", err
	}
	if response.BootstrapToken == "" {
		return "", fmt.Errorf("local API returned an empty bootstrap token")
	}
	return response.BootstrapToken, nil
}

func (c *Client) Stop(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, stopPath, nil, nil)
}

func (c *Client) TeamStatus(ctx context.Context) (team.Status, error) {
	var response team.Status
	return response, c.doJSON(ctx, http.MethodGet, teamPath+"/status", nil, &response)
}

func (c *Client) Missions(ctx context.Context) ([]model.MissionEnvelope, error) {
	var response MissionResponse
	return response.Missions, c.doJSON(ctx, http.MethodGet, "/api/v1/missions", nil, &response)
}
func (c *Client) IssueMission(ctx context.Context, input MissionRequest) (model.MissionEnvelope, error) {
	var response model.MissionEnvelope
	return response, c.doJSON(ctx, http.MethodPost, "/api/v1/missions", input, &response)
}
func (c *Client) Topology(ctx context.Context) (AgentTopologyResponse, error) {
	var response AgentTopologyResponse
	return response, c.doJSON(ctx, http.MethodGet, "/api/v1/agentteams/topology", nil, &response)
}
func (c *Client) Skills(ctx context.Context) ([]skillruntime.Definition, error) {
	var response SkillResponse
	return response.Skills, c.doJSON(ctx, http.MethodGet, "/api/v1/skills", nil, &response)
}
func (c *Client) Traces(ctx context.Context, missionID, after string) (TraceResponse, error) {
	var response TraceResponse
	values := url.Values{}
	if missionID != "" {
		values.Set("mission_id", missionID)
	}
	if after != "" {
		values.Set("after", after)
	}
	path := "/api/v1/traces"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return response, c.doJSON(ctx, http.MethodGet, path, nil, &response)
}
func (c *Client) Approvals(ctx context.Context) ([]model.ApprovalRequest, error) {
	var response ApprovalResponse
	return response.Approvals, c.doJSON(ctx, http.MethodGet, "/api/v1/approvals", nil, &response)
}
func (c *Client) DecideApproval(ctx context.Context, id, hash, decision, reason string, actor model.Actor) (model.ApprovalRequest, error) {
	var response model.ApprovalRequest
	return response, c.doJSON(ctx, http.MethodPost, "/api/v1/approvals/"+url.PathEscape(id)+"/decide", approvalDecisionRequest{PayloadSHA256: hash, Decision: decision, Reason: reason, Actor: actor}, &response)
}
func (c *Client) PreviewTransfer(ctx context.Context, archive []byte) (transferPreviewResponse, error) {
	var response transferPreviewResponse
	return response, c.doJSON(ctx, http.MethodPost, "/api/v1/transfers/preview", transferArchiveRequest{Archive: base64.StdEncoding.EncodeToString(archive)}, &response)
}
func (c *Client) ExportTransfer(ctx context.Context, request []byte) ([]byte, error) {
	var response struct {
		Archive string `json:"archive"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/transfers/export", json.RawMessage(request), &response); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(response.Archive)
}
func (c *Client) ApplyTransfer(ctx context.Context, hash string, actor model.Actor, confirmed bool) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/transfers/"+url.PathEscape(hash)+"/apply", transferApplyRequest{PreviewHash: hash, Actor: actor, Confirmed: confirmed}, nil)
}
func (c *Client) RebindAgent(ctx context.Context, id string, binding model.RuntimeBinding, actor model.Actor, confirmed bool) (model.RuntimeBinding, error) {
	var response model.RuntimeBinding
	binding.LogicalActorID = id
	return response, c.doJSON(ctx, http.MethodPost, "/api/v1/agents/"+url.PathEscape(id)+"/rebind", rebindRequest{Binding: binding, Actor: actor, Confirmed: confirmed}, &response)
}
func (c *Client) TeamSync(ctx context.Context) (teamsync.SyncReport, error) {
	var response teamsync.SyncReport
	return response, c.doJSON(ctx, http.MethodPost, teamPath+"/sync", nil, &response)
}
func (c *Client) TeamLeases(ctx context.Context) ([]model.Lease, error) {
	var response struct {
		Leases []model.Lease `json:"leases"`
	}
	return response.Leases, c.doJSON(ctx, http.MethodGet, teamPath+"/leases", nil, &response)
}
func (c *Client) TeamQueue(ctx context.Context) ([]teamsync.OutboxEntry, error) {
	var response struct {
		Queue []teamsync.OutboxEntry `json:"queue"`
	}
	return response.Queue, c.doJSON(ctx, http.MethodGet, teamPath+"/queue", nil, &response)
}
func (c *Client) TeamConflicts(ctx context.Context) ([]model.Conflict, error) {
	var response struct {
		Conflicts []model.Conflict `json:"conflicts"`
	}
	return response.Conflicts, c.doJSON(ctx, http.MethodGet, teamPath+"/conflicts", nil, &response)
}
func (c *Client) ResolveTeamConflict(ctx context.Context, id, action string) (team.PushResult, error) {
	return c.ResolveTeamConflictRequest(ctx, id, team.ConflictResolutionRequest{Action: action})
}
func (c *Client) ResolveTeamConflictRequest(ctx context.Context, id string, request team.ConflictResolutionRequest) (team.PushResult, error) {
	var response team.PushResult
	return response, c.doJSON(ctx, http.MethodPost, teamPath+"/conflicts/"+url.PathEscape(id)+"/resolve", struct {
		Action      string        `json:"action"`
		Replacement []model.Event `json:"replacement,omitempty"`
		Confirmed   bool          `json:"confirmed"`
	}{Action: request.Action, Replacement: request.Replacement, Confirmed: request.Confirmed}, &response)
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return fmt.Errorf("create API request: %w", err)
	}
	request.Header.Set(controlHeader, c.controlKey)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client().Do(request)
	if err != nil {
		return fmt.Errorf("call local API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeHTTPError(response)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

func (c *Client) client() *http.Client {
	if c.httpClient == nil {
		return http.DefaultClient
	}
	return c.httpClient
}

func decodeHTTPError(response *http.Response) error {
	var payload apiError
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return &HTTPError{StatusCode: response.StatusCode}
	}
	return &HTTPError{StatusCode: response.StatusCode, Code: payload.Code, Message: payload.Message}
}

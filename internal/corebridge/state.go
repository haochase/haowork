package corebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
	"go.yaml.in/yaml/v3"
)

type persistedState struct {
	SchemaVersion int                              `json:"schema_version"`
	Missions      map[string]model.MissionEnvelope `json:"missions"`
	Runs          map[string]persistedRun          `json:"runs"`
}

type persistedRun struct {
	Cursor              string                          `json:"cursor"`
	SourceEventIDs      []string                        `json:"source_event_ids"`
	ExecutionReceiptIDs []string                        `json:"execution_receipt_ids"`
	Request             executor.AgentTeamsStartRequest `json:"request"`
	LegacyUnverified    bool                            `json:"legacy_unverified,omitempty"`
}

const currentStateSchemaVersion = 2

var ErrLegacyRunEvidence = errors.New("legacy Core Bridge run has no structured execution receipt")

// State is the durable Core-side boundary used by the in-cluster bridge.
// Governance bindings use the normal app.Service and event store; only remote
// Mission inputs and opaque resume cursors live in the separate bridge file.
type State struct {
	mu              sync.Mutex
	root            string
	path            string
	events          eventstore.Store
	trace           trace.Store
	assignments     *executionreceipt.FileStore
	document        persistedState
	service         *app.Service
	projectID       string
	persistDocument func(persistedState) error
}

func OpenState(root string) (*State, error) {
	root, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, errors.New("Core Bridge state root is required")
	}
	assignments, err := executionreceipt.Open(root)
	if err != nil {
		return nil, err
	}
	state := &State{
		root: root, path: filepath.Join(root, ".haowork", "core-bridge", "state.json"),
		events: eventstore.New(root), trace: trace.New(root), assignments: assignments,
		document: persistedState{SchemaVersion: currentStateSchemaVersion, Missions: map[string]model.MissionEnvelope{}, Runs: map[string]persistedRun{}},
	}
	state.persistDocument = state.writeDocument
	governanceDir := filepath.Join(root, ".haowork")
	if err := os.MkdirAll(governanceDir, 0o700); err != nil {
		return nil, err
	}
	eventFile, err := os.OpenFile(filepath.Join(governanceDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := eventFile.Close(); err != nil {
		return nil, err
	}
	if err := state.reloadDocumentLocked(); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *State) Register(ctx context.Context, assignment executionreceipt.ExecutionAssignment) (executionreceipt.ExecutionAssignment, error) {
	return state.assignments.Register(ctx, assignment)
}

func (state *State) MarkDispatched(ctx context.Context, assignmentID, eventID string) (executionreceipt.ExecutionAssignment, error) {
	return state.assignments.MarkDispatched(ctx, assignmentID, eventID)
}

func (state *State) Wait(ctx context.Context, assignmentID string, timeout time.Duration) (executionreceipt.ExecutionReceipt, error) {
	return state.assignments.Wait(ctx, assignmentID, timeout)
}

func (state *State) RegisterMission(ctx context.Context, mission model.MissionEnvelope) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	candidateDocument := clonePersistedState(state.document)
	canonical, err := model.CanonicalizeMissionEnvelope(mission)
	if err != nil {
		return err
	}
	if mission.Hash == "" || mission.Hash != canonical.Hash || canonical.GoalVersion != 1 {
		return errors.New("Mission canonical hash or initial GoalVersion is invalid")
	}
	if existing, ok := candidateDocument.Missions[canonical.ID]; ok {
		left, _ := json.Marshal(existing)
		right, _ := json.Marshal(canonical)
		if string(left) != string(right) {
			return errors.New("Mission ID diverges from persisted Core Bridge state")
		}
	} else {
		candidateDocument.Missions[canonical.ID] = cloneMissionEnvelope(canonical)
	}
	if err := state.ensureGovernanceHistoryLocked(ctx, canonical); err != nil {
		return err
	}
	if err := state.persistDocument(clonePersistedState(candidateDocument)); err != nil {
		return err
	}
	state.document = candidateDocument
	return nil
}

func (state *State) ensureProjectManifestLocked(projectID string, goalVersion int, createdAt time.Time) error {
	path := filepath.Join(state.root, ".haowork", "manifest.yaml")
	if _, err := os.Stat(path); err == nil {
		manifest, err := capsule.Load(state.root)
		if err != nil {
			return err
		}
		if manifest.ProjectID != projectID || manifest.CurrentGoalVersion != goalVersion {
			return errors.New("Mission diverges from the Core Bridge project manifest")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	manifest := capsule.Manifest{
		ProtocolVersion: capsule.ProtocolVersion,
		ProjectID:       projectID, Name: "AgentTeams Core Bridge", CurrentGoalVersion: goalVersion,
		CreatedAt: createdAt.UTC(), CreatedBy: "USR-CORE-BRIDGE-OWNER",
	}
	encoded, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

// InitializeProject creates the minimum durable Core boundary needed by the
// MCP host before the first remote Mission arrives. It establishes project
// identity only; Mission, runtime binding, and Run authority remain separate.
func (state *State) InitializeProject(ctx context.Context, projectID string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.ensureProjectLocked(ctx, projectID, time.Now().UTC())
}

func (state *State) ensureProjectLocked(ctx context.Context, projectID string, createdAt time.Time) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("Core Bridge ProjectID is required")
	}
	if err := state.ensureProjectManifestLocked(projectID, 1, createdAt); err != nil {
		return err
	}
	events, err := state.events.ReadAll(ctx)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(events) == 0 {
		owner := model.Actor{ID: "USR-CORE-BRIDGE-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
		payload, _ := json.Marshal(model.ProjectInitialized{Name: "AgentTeams Core Bridge", Goal: model.GoalVersion{Version: 1, Statement: "Produce governed AgentTeams delivery evidence", CompletionCriteria: []string{"validated execution trace"}}})
		if _, err := state.events.Append(ctx, model.Event{ID: "EVT-CORE-BRIDGE-INIT", Type: "project.initialized", ProjectID: projectID, GoalVersion: 1, AggregateType: "project", AggregateID: projectID, Actor: owner, OccurredAt: createdAt.UTC(), Payload: payload}); err != nil {
			return err
		}
		events, err = state.events.ReadAll(ctx)
		if err != nil {
			return err
		}
	}
	projection, err := model.Reduce(events)
	if err != nil {
		return err
	}
	if projection.ProjectID != projectID || projection.Goal.Version != 1 {
		return errors.New("ProjectID diverges from Core Bridge governance history")
	}
	state.projectID = projectID
	state.service = app.New(projectID, 1, state.events, idgen.New(), systemClock{})
	return nil
}

func (state *State) ensureGovernanceHistoryLocked(ctx context.Context, mission model.MissionEnvelope) error {
	if err := state.ensureProjectLocked(ctx, mission.ProjectID, mission.IssuedAt); err != nil {
		return err
	}
	events, err := state.events.ReadAll(ctx)
	if err != nil {
		return err
	}
	owner := model.Actor{ID: "USR-CORE-BRIDGE-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	projection, err := model.Reduce(events)
	if err != nil {
		return err
	}
	if projection.ProjectID != mission.ProjectID {
		return errors.New("Mission project does not match Core Bridge governance history")
	}
	functions := make([]model.AgentFunction, 0, len(mission.RoleAssignments))
	for function := range mission.RoleAssignments {
		functions = append(functions, function)
	}
	sort.Slice(functions, func(i, j int) bool { return functions[i] < functions[j] })
	for _, function := range functions {
		agentID := mission.RoleAssignments[function]
		if existing, ok := projection.Agents[agentID]; ok {
			if existing.Function != function {
				return errors.New("Mission logical-agent function diverges from Core history")
			}
			continue
		}
		payload, _ := json.Marshal(model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: agentID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: function}})
		eventID := "EVT-CORE-BRIDGE-AGENT-" + agentID
		if _, err := state.events.AppendIfUnchanged(ctx, model.Event{ID: eventID, Type: "agent.identity.registered", ProjectID: mission.ProjectID, GoalVersion: 1, AggregateType: "agent", AggregateID: agentID, Actor: owner, OccurredAt: time.Now().UTC(), Payload: payload}, len(events)); err != nil {
			return err
		}
		events, err = state.events.ReadAll(ctx)
		if err != nil {
			return err
		}
		projection, err = model.Reduce(events)
		if err != nil {
			return err
		}
	}
	return nil
}

func (state *State) Mission(id string) (model.MissionEnvelope, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	mission, ok := state.document.Missions[id]
	if !ok {
		return model.MissionEnvelope{}, errors.New("Mission is not registered in Core Bridge")
	}
	return cloneMissionEnvelope(mission), nil
}

func (state *State) BindRuntimeTopology(ctx context.Context, bindings []model.RuntimeBinding, actor model.Actor) ([]model.RuntimeBinding, error) {
	state.mu.Lock()
	service := state.service
	if service == nil && len(state.document.Missions) > 0 {
		for _, mission := range state.document.Missions {
			if err := state.ensureGovernanceHistoryLocked(ctx, mission); err != nil {
				state.mu.Unlock()
				return nil, err
			}
			break
		}
		service = state.service
	}
	state.mu.Unlock()
	if service == nil {
		return nil, errors.New("Core Bridge Mission must be registered before runtime binding")
	}
	return service.BindRuntimeTopology(ctx, bindings, actor)
}

func (state *State) Cursor(runID string) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.document.Runs[runID].Cursor
}

func (state *State) RunRequest(runID string) (executor.AgentTeamsStartRequest, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	run, exists := state.document.Runs[runID]
	return run.Request, exists
}

func (state *State) RecordRun(request executor.AgentTeamsStartRequest, sourceEvents, receiptIDs []string, cursor string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(receiptIDs) != 1 || !executionreceipt.ValidReceiptID(receiptIDs[0]) || len(sourceEvents) != 1 || sourceEvents[0] != receiptIDs[0] {
		return errors.New("Core Bridge run requires exactly one structured execution receipt")
	}
	candidateDocument := clonePersistedState(state.document)
	if existing, exists := state.document.Runs[request.RunID]; exists {
		candidate := persistedRun{Cursor: cursor, SourceEventIDs: append([]string(nil), sourceEvents...), ExecutionReceiptIDs: append([]string(nil), receiptIDs...), Request: request}
		if !reflect.DeepEqual(existing, candidate) {
			return errors.New("RunID conflicts with persisted Core Bridge evidence")
		}
		return nil
	}
	run := persistedRun{Cursor: cursor, SourceEventIDs: append([]string(nil), sourceEvents...), ExecutionReceiptIDs: append([]string(nil), receiptIDs...), Request: request}
	candidateDocument.Runs[request.RunID] = run
	if err := state.persistDocument(clonePersistedState(candidateDocument)); err != nil {
		return err
	}
	state.document = candidateDocument
	return nil
}

func clonePersistedState(source persistedState) persistedState {
	result := persistedState{SchemaVersion: source.SchemaVersion, Missions: make(map[string]model.MissionEnvelope, len(source.Missions)), Runs: make(map[string]persistedRun, len(source.Runs))}
	for id, mission := range source.Missions {
		result.Missions[id] = cloneMissionEnvelope(mission)
	}
	for id, run := range source.Runs {
		run.SourceEventIDs = append([]string(nil), run.SourceEventIDs...)
		run.ExecutionReceiptIDs = append([]string(nil), run.ExecutionReceiptIDs...)
		result.Runs[id] = run
	}
	return result
}

// State returns the current Core projection plus authenticated remote
// Mission and Run bindings. MCP request arguments never populate this view.
func (state *State) State(ctx context.Context) (model.ProjectState, error) {
	state.mu.Lock()
	if err := state.reloadDocumentLocked(); err != nil {
		state.mu.Unlock()
		return model.ProjectState{}, err
	}
	missions := make(map[string]model.MissionEnvelope, len(state.document.Missions))
	for id, mission := range state.document.Missions {
		missions[id] = cloneMissionEnvelope(mission)
	}
	runs := make(map[string]persistedRun, len(state.document.Runs))
	for id, run := range state.document.Runs {
		runs[id] = run
	}
	state.mu.Unlock()
	history, err := state.events.ReadAll(ctx)
	if err != nil {
		return model.ProjectState{}, err
	}
	projection, err := model.Reduce(history)
	if err != nil {
		return model.ProjectState{}, err
	}
	if projection.Missions == nil {
		projection.Missions = make(map[string]model.MissionEnvelope)
	}
	if projection.Tasks == nil {
		projection.Tasks = make(map[string]model.Task)
	}
	if projection.Runs == nil {
		projection.Runs = make(map[string]model.Run)
	}
	for id, mission := range missions {
		projection.Missions[id] = cloneMissionEnvelope(mission)
	}
	for id, persisted := range runs {
		if persisted.LegacyUnverified {
			continue
		}
		request := persisted.Request
		projection.Tasks[request.TaskID] = model.Task{ID: request.TaskID, GoalVersion: request.GoalVersion, Title: "AgentTeams governed run", Status: model.StatusRunning, LastRunID: id}
		projection.Runs[id] = model.Run{ID: id, TaskID: request.TaskID, GoalVersion: request.GoalVersion, Executor: "agentteams", ActorID: request.LogicalActorID, ContextID: request.ContextID, ContextHash: request.ContextHash, AdapterCursor: persisted.Cursor, Status: model.StatusRunning}
	}
	return projection, nil
}

// reloadDocumentLocked makes a long-lived MCP reader observe the Mission and
// Run records written by the separate Core Bridge process on the shared PVC.
func (state *State) reloadDocumentLocked() error {
	content, err := os.ReadFile(state.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var document persistedState
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if document.Missions == nil || document.Runs == nil {
		return errors.New("Core Bridge state contract is invalid")
	}
	switch document.SchemaVersion {
	case 1:
		for id, run := range document.Runs {
			run.LegacyUnverified = true
			document.Runs[id] = run
		}
		document.SchemaVersion = currentStateSchemaVersion
	case currentStateSchemaVersion:
		for _, run := range document.Runs {
			if !run.LegacyUnverified && !validPersistedReceiptRun(run) {
				return errors.New("Core Bridge state contains unverifiable run evidence")
			}
		}
	default:
		return errors.New("Core Bridge state contract is invalid")
	}
	state.document = document
	return nil
}

func (state *State) TraceStore() trace.Store { return state.trace }

func (state *State) CandidateTraceExists(ctx context.Context, runID string) (bool, error) {
	records, err := state.trace.ReadAll(ctx)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.RunID == runID && record.Candidate {
			return true, nil
		}
	}
	return false, nil
}

func (state *State) ReceiptExists(ctx context.Context, assignmentID string) (bool, error) {
	_, err := state.assignments.Receipt(ctx, assignmentID)
	if errors.Is(err, executionreceipt.ErrReceiptNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (state *State) Evidence(ctx context.Context, runID string) (RunEvidence, error) {
	state.mu.Lock()
	run, ok := state.document.Runs[runID]
	state.mu.Unlock()
	if !ok {
		return RunEvidence{}, errors.New("run evidence is not found")
	}
	if run.LegacyUnverified || !validPersistedReceiptRun(run) {
		return RunEvidence{}, ErrLegacyRunEvidence
	}
	events, err := state.events.ReadAll(ctx)
	if err != nil {
		return RunEvidence{}, err
	}
	traces, err := state.trace.ReadAll(ctx)
	if err != nil {
		return RunEvidence{}, err
	}
	traceIDs := make([]string, 0)
	artifacts := make([]RunArtifact, 0)
	seenArtifacts := make(map[string]struct{})
	for _, record := range traces {
		if record.RunID != runID {
			continue
		}
		traceIDs = append(traceIDs, record.ID)
		for _, artifact := range record.Artifacts {
			key := artifact.URI + "\x00" + artifact.SHA256
			if _, exists := seenArtifacts[key]; exists {
				continue
			}
			seenArtifacts[key] = struct{}{}
			artifacts = append(artifacts, RunArtifact{Kind: artifact.Kind, Key: artifact.URI, SHA256: artifact.SHA256, EnvironmentID: artifact.EnvironmentID, Size: artifact.Size})
		}
	}
	sort.Strings(traceIDs)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Key < artifacts[j].Key })
	return RunEvidence{RunID: runID, RuntimeBindingRevision: run.Request.RuntimeBindingRevision, Cursor: run.Cursor, SourceEventIDs: append([]string(nil), run.SourceEventIDs...), ExecutionReceiptIDs: append([]string(nil), run.ExecutionReceiptIDs...), CoreHistorySHA256: digestJSON(events), TraceSHA256: digestJSON(traces), TraceIDs: traceIDs, Artifacts: artifacts}, nil
}

func cloneMissionEnvelope(source model.MissionEnvelope) model.MissionEnvelope {
	result := source
	result.GovernanceTaskIDs = append([]string(nil), source.GovernanceTaskIDs...)
	result.CompletionCriteria = append([]string(nil), source.CompletionCriteria...)
	result.AllowedScopes = append([]string(nil), source.AllowedScopes...)
	result.AllowedSkills = append([]model.MissionSkillGrant(nil), source.AllowedSkills...)
	if source.RoleAssignments != nil {
		result.RoleAssignments = make(map[model.AgentFunction]string, len(source.RoleAssignments))
		for function, actorID := range source.RoleAssignments {
			result.RoleAssignments[function] = actorID
		}
	}
	return result
}

func validPersistedReceiptRun(run persistedRun) bool {
	return len(run.ExecutionReceiptIDs) == 1 && executionreceipt.ValidReceiptID(run.ExecutionReceiptIDs[0]) && len(run.SourceEventIDs) == 1 && run.SourceEventIDs[0] == run.ExecutionReceiptIDs[0]
}

func (state *State) writeDocument(document persistedState) error {
	if err := os.MkdirAll(filepath.Dir(state.path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	temporary := state.path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, state.path); err != nil {
		if removeErr := os.Remove(state.path); removeErr != nil && !os.IsNotExist(removeErr) {
			return err
		}
		if err := os.Rename(temporary, state.path); err != nil {
			return err
		}
	}
	return nil
}

func digestJSON(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

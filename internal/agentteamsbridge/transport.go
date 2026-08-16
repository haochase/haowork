package agentteamsbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

type TransportConfig struct {
	Orchestrator     Orchestrator
	Matrix           MatrixClient
	Artifacts        ArtifactStore
	MaxArtifactBytes int64
	Mission          func(string) (model.MissionEnvelope, error)
	Trace            trace.Store
	RuntimeBindings  RuntimeBindingStore
	BindingActor     model.Actor
	// EmptyMatrixPollLimit permits a production transport to wait briefly for
	// a newly delegated Matrix event to become visible. Zero keeps unit and
	// migration transports single-pass.
	EmptyMatrixPollLimit    int
	EmptyMatrixPollInterval time.Duration
	// ExpectedEnvironmentID is set by the official production constructor and
	// prevents a governed request from crossing into another security zone.
	ExpectedEnvironmentID string
	// Ready performs mandatory production dependency checks before delegation.
	// Unit-test transports leave it nil.
	Ready func(context.Context) error
}

// RuntimeBindingStore persists control-plane topology through Core's governed event path.
type RuntimeBindingStore interface {
	BindRuntimeTopology(context.Context, []model.RuntimeBinding, model.Actor) ([]model.RuntimeBinding, error)
}

type Transport struct{ config TransportConfig }

func NewTransport(config TransportConfig) *Transport { return &Transport{config: config} }

// NewLegacyTransportForMigrationTest preserves the retired private adapters
// exclusively for the explicit migration E2E test. It is not a production
// construction path.
func NewLegacyTransportForMigrationTest(config TransportConfig, matrixURL, artifactURL string, client *http.Client, identity string) *Transport {
	if config.Matrix == nil {
		config.Matrix = NewHTTPMatrixClient(matrixURL, client, identity)
	}
	if config.Artifacts == nil {
		config.Artifacts = NewHTTPArtifactStore(artifactURL, client, identity)
	}
	return NewTransport(config)
}

func (transport *Transport) Start(ctx context.Context, request executor.AgentTeamsStartRequest) (executor.AgentTeamsSession, error) {
	if transport == nil || transport.config.Orchestrator == nil || transport.config.Matrix == nil || transport.config.Artifacts == nil || transport.config.Mission == nil || transport.config.RuntimeBindings == nil {
		return nil, errors.New("real AgentTeams bridge dependencies are required")
	}
	if expected := strings.TrimSpace(transport.config.ExpectedEnvironmentID); expected != "" && strings.TrimSpace(request.EnvironmentID) != expected {
		return nil, errors.New("AgentTeams start request environment does not match the production transport")
	}
	if transport.config.Ready != nil {
		if err := transport.config.Ready(ctx); err != nil {
			return nil, err
		}
	}
	if request.MissionID == "" || request.StepID == "" || request.LogicalActorID == "" || request.RuntimeBindingRevision < 1 || request.AgentFunction == "" || request.EnvironmentID == "" || request.AgentTeamsInstanceID == "" {
		return nil, errors.New("AgentTeams start request is missing governance binding")
	}
	mission, err := transport.config.Mission(request.MissionID)
	if err != nil {
		return nil, err
	}
	if mission.ID != request.MissionID || mission.GoalVersion != request.GoalVersion || mission.ContextID != request.ContextID || mission.ContextHash != request.ContextHash || mission.EnvironmentID != request.EnvironmentID || (strings.TrimSpace(transport.config.ExpectedEnvironmentID) != "" && mission.EnvironmentID != strings.TrimSpace(transport.config.ExpectedEnvironmentID)) {
		return nil, errors.New("mission does not match governed execution request")
	}
	document, err := json.Marshal(mission)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(document)
	artifactRef, err := transport.config.Artifacts.Upload(ctx, "missions/"+mission.ID+".json", document, hex.EncodeToString(digest[:]))
	if err != nil {
		return nil, err
	}
	topology, err := transport.config.Orchestrator.EnsureMissionTeam(ctx, mission)
	if err != nil {
		return nil, err
	}
	if err := validateTopology(topology, request, mission); err != nil {
		return nil, err
	}
	if selector, ok := transport.config.Matrix.(interface{ SelectRoom(string) error }); ok {
		if err := selector.SelectRoom(topology.ManagerRoomID); err != nil {
			return nil, err
		}
	}
	persisted, err := transport.config.RuntimeBindings.BindRuntimeTopology(ctx, topologyRuntimeBindings(topology, mission, request), transport.config.BindingActor)
	if err != nil {
		return nil, err
	}
	if len(persisted) != 5 {
		return nil, errors.New("AgentTeams runtime topology bindings were not fully persisted")
	}
	managerBound := false
	for _, binding := range persisted {
		if binding.LogicalActorID != request.LogicalActorID {
			continue
		}
		if binding.RuntimePrincipalID != topology.ManagerPrincipalID || binding.Revision < 1 {
			return nil, errors.New("AgentTeams Manager runtime binding was not persisted")
		}
		request.RuntimePrincipalID = binding.RuntimePrincipalID
		request.RuntimeBindingRevision = binding.Revision
		managerBound = true
		break
	}
	if !managerBound {
		return nil, errors.New("AgentTeams Manager runtime binding is missing after persistence")
	}
	if err := transport.config.Matrix.Send(ctx, topology.ManagerRoomID, MatrixOutbound{
		MissionID: mission.ID, RunID: request.RunID, WorkItemID: request.WorkItemID, WorkspaceDigest: hex.EncodeToString(digest[:]), ArtifactRef: artifactRef,
		Artifact: MatrixArtifact{
			Kind: "mission", URI: artifactRef, SHA256: hex.EncodeToString(digest[:]), EnvironmentID: mission.EnvironmentID, Size: int64(len(document)),
		},
	}); err != nil {
		return nil, err
	}
	session := &session{transport: transport, request: request, missionID: mission.ID, roomID: topology.ManagerRoomID, allowedSenders: topologySenders(topology), errors: make(chan error, 1)}
	if err := session.appendTrace(ctx, "bridge.delegated", request.RunID+":"+request.WorkItemID+":bridge.delegated", "delegated", "", "", "", nil, nil); err != nil {
		return nil, err
	}
	return session, nil
}

type senderIdentity struct {
	role     string
	function model.AgentFunction
}

func topologySenders(topology RuntimeTopology) map[string]senderIdentity {
	return map[string]senderIdentity{
		topology.ManagerPrincipalID:                         {role: string(model.FunctionManager), function: model.FunctionManager},
		topology.LeaderPrincipalID:                          {role: string(model.FunctionDeliveryLeader), function: model.FunctionDeliveryLeader},
		topology.WorkerPrincipalIDs[model.FunctionResearch]: {role: string(model.FunctionResearch), function: model.FunctionResearch},
		topology.WorkerPrincipalIDs[model.FunctionBuild]:    {role: string(model.FunctionBuild), function: model.FunctionBuild},
		topology.WorkerPrincipalIDs[model.FunctionVerify]:   {role: string(model.FunctionVerify), function: model.FunctionVerify},
		topology.HumanPrincipalID:                           {role: "human"},
	}
}

func topologyRuntimeBindings(topology RuntimeTopology, mission model.MissionEnvelope, request executor.AgentTeamsStartRequest) []model.RuntimeBinding {
	principalFor := map[model.AgentFunction]string{
		model.FunctionManager:        topology.ManagerPrincipalID,
		model.FunctionDeliveryLeader: topology.LeaderPrincipalID,
		model.FunctionResearch:       topology.WorkerPrincipalIDs[model.FunctionResearch],
		model.FunctionBuild:          topology.WorkerPrincipalIDs[model.FunctionBuild],
		model.FunctionVerify:         topology.WorkerPrincipalIDs[model.FunctionVerify],
	}
	bindings := make([]model.RuntimeBinding, 0, len(principalFor))
	for _, function := range []model.AgentFunction{model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify} {
		bindings = append(bindings, model.RuntimeBinding{
			LogicalActorID: mission.RoleAssignments[function], EnvironmentID: request.EnvironmentID, AgentTeamsInstanceID: request.AgentTeamsInstanceID,
			RuntimePrincipalID: principalFor[function], HumanPrincipalID: topology.HumanPrincipalID, TeamRoomID: topology.TeamRoomID,
		})
		if function == model.FunctionManager || function == model.FunctionDeliveryLeader {
			bindings[len(bindings)-1].LeaderRoomID = topology.LeaderRoomID
		}
	}
	return bindings
}

func validateTopology(topology RuntimeTopology, request executor.AgentTeamsStartRequest, mission model.MissionEnvelope) error {
	if topology.MissionID != mission.ID || topology.TeamName == "" || topology.ManagerRoomID == "" || topology.LeaderRoomID == "" || topology.TeamRoomID == "" || topology.ManagerPrincipalID != request.RuntimePrincipalID || topology.LeaderPrincipalID == "" || topology.HumanPrincipalID == "" {
		return errors.New("AgentTeams team topology does not match the active runtime binding")
	}
	for _, function := range []model.AgentFunction{model.FunctionResearch, model.FunctionBuild, model.FunctionVerify} {
		if topology.WorkerPrincipalIDs[function] == "" {
			return errors.New("AgentTeams team topology omits a required worker principal")
		}
	}
	principals := []string{topology.ManagerPrincipalID, topology.LeaderPrincipalID, topology.WorkerPrincipalIDs[model.FunctionResearch], topology.WorkerPrincipalIDs[model.FunctionBuild], topology.WorkerPrincipalIDs[model.FunctionVerify], topology.HumanPrincipalID}
	seen := make(map[string]struct{}, len(principals))
	for _, principal := range principals {
		if _, exists := seen[principal]; exists {
			return errors.New("AgentTeams team topology reuses a principal across governance roles")
		}
		seen[principal] = struct{}{}
	}
	return nil
}

type session struct {
	transport      *Transport
	request        executor.AgentTeamsStartRequest
	missionID      string
	roomID         string
	allowedSenders map[string]senderIdentity
	errors         chan error
	once           sync.Once
	cancelErr      error
}

// BoundRequest returns the request after the observed topology supplied its
// persisted runtime-binding revision.
func (session *session) BoundRequest() executor.AgentTeamsStartRequest {
	if session == nil {
		return executor.AgentTeamsStartRequest{}
	}
	return session.request
}

func (session *session) Events(ctx context.Context, cursor string) <-chan executor.AgentTeamsEvent {
	output := make(chan executor.AgentTeamsEvent)
	go func() {
		defer close(output)
		seen := make(map[string]struct{})
		emptyPolls := 0
		for {
			page, err := session.transport.config.Matrix.Sync(ctx, cursor)
			if err != nil {
				session.recordError(err)
				return
			}
			emitted := false
			for index, event := range page.Events {
				if event.ID == "" {
					session.recordError(errors.New("Matrix event id is required"))
					return
				}
				if event.MissionID != "" && (event.MissionID != session.missionID || event.RunID != session.request.RunID || event.WorkItemID != session.request.WorkItemID) {
					continue
				}
				if err := session.bindAndValidateMatrixEvent(&event); err != nil {
					session.recordError(err)
					return
				}
				if _, ok := seen[event.ID]; ok {
					continue
				}
				if event.WorkspaceDigest == "" {
					session.recordError(errors.New("Matrix event workspace digest is required"))
					return
				}
				seen[event.ID] = struct{}{}
				artifacts, err := DownloadMatrixArtifacts(ctx, session.transport.config.Artifacts, event.Artifacts, session.request.EnvironmentID, session.transport.config.MaxArtifactBytes)
				if err != nil {
					session.recordError(err)
					return
				}
				if err := session.appendTrace(ctx, event.Kind, event.ID, "observed", page.NextCursor, event.Summary, event.SenderID, artifacts, event.Artifacts); err != nil {
					session.errors <- err
					return
				}
				select {
				case output <- executor.AgentTeamsEvent{RunID: session.request.RunID, StepID: session.request.StepID, Kind: event.Kind, Cursor: matrixEventCursor(page.NextCursor, index, event.ID), SourceEventID: event.ID, AdapterCursor: page.NextCursor, Summary: event.Summary, Artifacts: artifacts, WorkspaceDigest: event.WorkspaceDigest, ActorID: event.SenderID, ActorRole: event.SenderRole}:
					emitted = true
				case <-ctx.Done():
					return
				}
			}
			if !page.More {
				if emitted || session.transport.config.EmptyMatrixPollLimit <= 0 || emptyPolls >= session.transport.config.EmptyMatrixPollLimit {
					return
				}
				emptyPolls++
				if page.NextCursor != "" && page.NextCursor != cursor {
					cursor = page.NextCursor
				}
				interval := session.transport.config.EmptyMatrixPollInterval
				if interval <= 0 {
					interval = time.Second
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(interval):
				}
				continue
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				session.recordError(errors.New("Matrix sync cursor did not advance"))
				return
			}
			cursor = page.NextCursor
		}
	}()
	return output
}

func (session *session) bindAndValidateMatrixEvent(event *MatrixEvent) error {
	if event == nil {
		return errors.New("Matrix event is required")
	}
	if strings.TrimSpace(event.RoomID) == "" || event.RoomID != session.roomID {
		return errors.New("Matrix event room does not match the delegated leader room")
	}
	senderID := strings.TrimSpace(event.SenderID)
	expected, exists := session.allowedSenders[senderID]
	if !exists || senderID == "" {
		return errors.New("Matrix event sender is not in the delegated topology")
	}
	if strings.TrimSpace(event.SenderRole) == "" {
		event.SenderRole = expected.role
	}
	if event.AgentFunction == "" {
		event.AgentFunction = expected.function
	}
	if !strings.EqualFold(strings.TrimSpace(event.SenderRole), expected.role) {
		return errors.New("Matrix event sender role does not match the delegated topology")
	}
	if event.AgentFunction != expected.function {
		return errors.New("Matrix event agent function does not match the delegated topology")
	}
	return nil
}

func matrixEventCursor(pageCursor string, index int, eventID string) string {
	return fmt.Sprintf("%s#%06d:%s", pageCursor, index, eventID)
}

func (session *session) Errors(context.Context) <-chan error { return session.errors }

func (session *session) recordError(err error) {
	if err == nil {
		return
	}
	select {
	case session.errors <- err:
	default:
	}
}

func (session *session) appendTrace(ctx context.Context, eventType, sourceID, status, cursor, summary, senderID string, artifacts []model.ArtifactRef, matrixArtifacts []MatrixArtifact) error {
	if session.transport.config.Trace == nil {
		return nil
	}
	record := trace.Envelope{
		ID: session.request.RunID + ":" + sourceID, MissionID: session.request.MissionID, GovernanceTaskID: session.request.TaskID,
		WorkItemID: session.request.WorkItemID, RunID: session.request.RunID, LogicalActorID: session.request.LogicalActorID,
		RuntimeBindingRevision: session.request.RuntimeBindingRevision, AgentFunction: session.request.AgentFunction, EnvironmentID: session.request.EnvironmentID,
		AgentTeamsInstanceID: session.request.AgentTeamsInstanceID, RoomID: session.roomID, SenderID: stableSenderID(senderID), SourceEventID: sourceID, SourceEventType: eventType,
		SourceSystem: "matrix", Cursor: cursor, ArtifactRefs: artifacts, Status: status, StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(1, 0).UTC(),
	}
	for _, artifact := range matrixArtifacts {
		record.Artifacts = append(record.Artifacts, trace.ArtifactObservation{Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256, EnvironmentID: artifact.EnvironmentID, Size: artifact.Size})
	}
	if summary != "" {
		digest := sha256.Sum256([]byte(summary))
		record.SummarySHA256 = hex.EncodeToString(digest[:])
	}
	_, err := session.transport.config.Trace.AppendIdempotent(ctx, record)
	return err
}

func stableSenderID(senderID string) string {
	if senderID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(senderID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (session *session) Cancel(ctx context.Context) error {
	session.once.Do(func() {
		if stopper, ok := session.transport.config.Matrix.(interface {
			Stop(context.Context, string) error
		}); ok {
			if err := stopper.Stop(ctx, session.roomID); err != nil {
				session.cancelErr = err
				return
			}
		}
		session.cancelErr = session.transport.config.Orchestrator.StopMissionTeam(ctx, session.missionID)
	})
	return session.cancelErr
}

var _ executor.AgentTeamsTransport = (*Transport)(nil)

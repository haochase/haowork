package agentteamsbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

type TransportConfig struct {
	Orchestrator       Orchestrator
	Matrix             MatrixClient
	Artifacts          ArtifactStore
	MaxArtifactBytes   int64
	Mission            func(string) (model.MissionEnvelope, error)
	Trace              trace.Store
	RuntimeBindings    RuntimeBindingStore
	Assignments        AssignmentStore
	ReceiptWaitTimeout time.Duration
	BindingActor       model.Actor
	// EmptyMatrixPollLimit permits a production transport to wait briefly for
	// a newly delegated Matrix event to become visible. Zero keeps unit and
	// migration transports single-pass.
	EmptyMatrixPollLimit     int
	EmptyMatrixPollInterval  time.Duration
	MatrixCheckpointRequired bool
	// ExpectedEnvironmentID is set by the official production constructor and
	// prevents a governed request from crossing into another security zone.
	ExpectedEnvironmentID string
	// Ready performs mandatory production dependency checks before delegation.
	// Unit-test transports leave it nil.
	Ready func(context.Context) error
}

type AssignmentStore interface {
	Register(context.Context, executionreceipt.ExecutionAssignment) (executionreceipt.ExecutionAssignment, error)
	MarkDispatched(context.Context, string, string) (executionreceipt.ExecutionAssignment, error)
	Wait(context.Context, string, time.Duration) (executionreceipt.ExecutionReceipt, error)
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
	if transport == nil || transport.config.Orchestrator == nil || transport.config.Matrix == nil || transport.config.Artifacts == nil || transport.config.Mission == nil || transport.config.RuntimeBindings == nil || transport.config.Assignments == nil {
		return nil, errors.New("real AgentTeams bridge dependencies are required")
	}
	if expected := strings.TrimSpace(transport.config.ExpectedEnvironmentID); expected != "" && strings.TrimSpace(request.EnvironmentID) != expected {
		return nil, errors.New("AgentTeams start request environment does not match the production transport")
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
	workspaceDigest := hex.EncodeToString(digest[:])
	artifactRef, err := transport.config.Artifacts.Upload(ctx, "missions/"+mission.ID+".json", document, workspaceDigest)
	if err != nil {
		return nil, err
	}
	artifactURI := artifactRef
	if resolver, ok := transport.config.Artifacts.(interface{ ArtifactURI(string) (string, error) }); ok {
		artifactURI, err = resolver.ArtifactURI(artifactRef)
		if err != nil {
			return nil, err
		}
	}
	topology, err := transport.config.Orchestrator.EnsureMissionTeam(ctx, mission)
	if err != nil {
		return nil, err
	}
	if err := validateTopology(topology, request, mission); err != nil {
		return nil, err
	}
	if binder, ok := transport.config.Matrix.(HumanMatrixBinder); ok {
		if topology.HumanName == "" || topology.HumanUID == "" || topology.HumanPrincipalID == "" {
			return nil, errors.New("AgentTeams Human runtime name is missing")
		}
		if err := binder.BindHuman(ctx, HumanMatrixIdentity{Name: topology.HumanName, UID: topology.HumanUID, PrincipalID: topology.HumanPrincipalID}); err != nil {
			return nil, err
		}
	}
	if selector, ok := transport.config.Matrix.(interface{ SelectRoom(string) error }); ok {
		if err := selector.SelectRoom(topology.LeaderRoomID); err != nil {
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
	leaderBound := false
	var leaderIdentity senderIdentity
	for _, binding := range persisted {
		if binding.LogicalActorID == request.LogicalActorID {
			if binding.RuntimePrincipalID != topology.ManagerPrincipalID || binding.Revision < 1 {
				return nil, errors.New("AgentTeams Manager runtime binding was not persisted")
			}
			request.RuntimePrincipalID = binding.RuntimePrincipalID
			request.RuntimeBindingRevision = binding.Revision
			managerBound = true
		}
		if binding.LogicalActorID == mission.RoleAssignments[model.FunctionDeliveryLeader] {
			if binding.RuntimePrincipalID != topology.LeaderPrincipalID || binding.Revision < 1 {
				return nil, errors.New("AgentTeams Delivery Leader runtime binding was not persisted")
			}
			leaderIdentity = senderIdentity{
				role: string(model.FunctionDeliveryLeader), function: model.FunctionDeliveryLeader,
				logicalActorID: binding.LogicalActorID, bindingRevision: binding.Revision,
			}
			leaderBound = true
		}
	}
	if !managerBound {
		return nil, errors.New("AgentTeams Manager runtime binding is missing after persistence")
	}
	if !leaderBound {
		return nil, errors.New("AgentTeams Delivery Leader runtime binding is missing after persistence")
	}
	if transport.config.Ready != nil {
		if err := transport.config.Ready(ctx); err != nil {
			return nil, err
		}
	}
	missionArtifact := MatrixArtifact{
		Kind: "mission", URI: artifactURI, SHA256: workspaceDigest, EnvironmentID: mission.EnvironmentID, Size: int64(len(document)),
	}
	assignment := executionreceipt.ExecutionAssignment{
		ID:        deterministicAssignmentID(mission.ID, request.RunID, request.WorkItemID),
		ProjectID: mission.ProjectID, MissionID: mission.ID, GovernanceTaskID: request.TaskID,
		RunID: request.RunID, WorkItemID: request.WorkItemID, EnvironmentID: request.EnvironmentID,
		AgentTeamsInstanceID: request.AgentTeamsInstanceID, LeaderLogicalActorID: leaderIdentity.logicalActorID,
		LeaderRuntimePrincipalID: topology.LeaderPrincipalID, AssignmentRevision: 1,
		RuntimeBindingRevision: leaderIdentity.bindingRevision, GoalVersion: request.GoalVersion,
		ContextID: request.ContextID, ContextHash: request.ContextHash, LeaseID: mission.LeaseID,
		MissionArtifactURI: artifactURI, MissionArtifactSHA256: workspaceDigest, MatrixRoomID: topology.LeaderRoomID,
		State: executionreceipt.AssignmentPrepared, CreatedAt: mission.IssuedAt, Deadline: mission.Deadline,
	}
	candidateAssignment := assignment
	assignment, err = transport.config.Assignments.Register(ctx, candidateAssignment)
	if err != nil {
		assignment, err = recoverAssignment(ctx, transport.config.Assignments, candidateAssignment, err)
		if err != nil {
			return nil, err
		}
	}
	outbound := MatrixOutbound{
		AssignmentID: assignment.ID, MissionID: mission.ID, RunID: request.RunID, WorkItemID: request.WorkItemID,
		WorkspaceDigest: workspaceDigest, ArtifactRef: artifactURI, Artifact: missionArtifact,
		AllowedSkills: append([]model.MissionSkillGrant(nil), mission.AllowedSkills...),
	}
	dispatch := MatrixDispatch{EventID: assignment.MatrixDispatchEventID}
	if assignment.State == executionreceipt.AssignmentPrepared {
		dispatch, err = transport.config.Matrix.Send(ctx, topology.LeaderRoomID, outbound)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(dispatch.EventID) == "" {
			return nil, errors.New("Matrix dispatch event ID is required")
		}
		assignment, err = transport.config.Assignments.MarkDispatched(ctx, assignment.ID, dispatch.EventID)
		if err != nil {
			return nil, err
		}
	} else if (assignment.State != executionreceipt.AssignmentDispatched && assignment.State != executionreceipt.AssignmentReportSubmitted) || strings.TrimSpace(dispatch.EventID) == "" {
		return nil, errors.New("existing execution assignment is not resumable")
	}
	session := &session{transport: transport, request: request, assignment: assignment, leaderIdentity: leaderIdentity, errors: make(chan error, 1)}
	if err := session.appendDispatchTrace(ctx, dispatch.EventID, missionArtifact); err != nil {
		return nil, err
	}
	return session, nil
}

func recoverAssignment(ctx context.Context, store AssignmentStore, candidate executionreceipt.ExecutionAssignment, registerErr error) (executionreceipt.ExecutionAssignment, error) {
	if !errors.Is(registerErr, executionreceipt.ErrAssignmentConflict) {
		return executionreceipt.ExecutionAssignment{}, registerErr
	}
	reader, ok := store.(interface {
		Assignment(context.Context, string) (executionreceipt.ExecutionAssignment, error)
	})
	if !ok {
		return executionreceipt.ExecutionAssignment{}, registerErr
	}
	stored, err := reader.Assignment(ctx, candidate.ID)
	if err != nil {
		return executionreceipt.ExecutionAssignment{}, err
	}
	candidate.State = stored.State
	candidate.MatrixDispatchEventID = stored.MatrixDispatchEventID
	if candidate != stored {
		return executionreceipt.ExecutionAssignment{}, registerErr
	}
	return stored, nil
}

func deterministicAssignmentID(missionID, runID, workItemID string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{strings.TrimSpace(missionID), strings.TrimSpace(runID), strings.TrimSpace(workItemID)}, "\x00")))
	return "ASN-" + hex.EncodeToString(digest[:16])
}

type senderIdentity struct {
	role            string
	function        model.AgentFunction
	logicalActorID  string
	bindingRevision int
}

type safeSessionError string

func (failure safeSessionError) Error() string    { return string(failure) }
func (failure safeSessionError) SafeCode() string { return string(failure) }

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
	assignment     executionreceipt.ExecutionAssignment
	leaderIdentity senderIdentity
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
		receipt, err := session.transport.config.Assignments.Wait(ctx, session.assignment.ID, session.transport.config.ReceiptWaitTimeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, executionreceipt.ErrReceiptNotFound) {
				session.recordError(safeSessionError("execution_report_missing"))
				return
			}
			session.recordError(err)
			return
		}
		if strings.TrimSpace(receipt.ID) == "" || receipt.AssignmentID != session.assignment.ID || receipt.Report.AssignmentID != session.assignment.ID {
			session.recordError(safeSessionError("execution_report_invalid"))
			return
		}
		matrixArtifacts := make([]MatrixArtifact, 0, len(receipt.Report.Artifacts))
		for _, artifact := range receipt.Report.Artifacts {
			matrixArtifacts = append(matrixArtifacts, MatrixArtifact{Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256, EnvironmentID: artifact.EnvironmentID, Size: artifact.Size})
		}
		artifacts, err := DownloadMatrixArtifacts(ctx, session.transport.config.Artifacts, matrixArtifacts, session.request.EnvironmentID, session.transport.config.MaxArtifactBytes)
		if err != nil {
			session.recordError(err)
			return
		}
		if err := session.appendReceiptTrace(ctx, receipt); err != nil {
			session.recordError(err)
			return
		}
		event := executor.AgentTeamsEvent{
			RunID: session.request.RunID, StepID: session.request.StepID, Kind: "execution_receipt",
			Cursor: receipt.ID, SourceEventID: receipt.ID, AdapterCursor: receipt.ID, Summary: receipt.Report.Status,
			Artifacts: artifacts, WorkspaceDigest: session.assignment.MissionArtifactSHA256,
			ActorID: session.assignment.LeaderRuntimePrincipalID, ActorRole: string(model.FunctionDeliveryLeader),
		}
		select {
		case output <- event:
		case <-ctx.Done():
		}
	}()
	return output
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

func (session *session) appendDispatchTrace(ctx context.Context, eventID string, artifact MatrixArtifact) error {
	if session.transport.config.Trace == nil {
		return nil
	}
	record := trace.Envelope{
		ID: session.request.RunID + ":matrix-dispatch", AssignmentID: session.assignment.ID, AssignmentRevision: session.assignment.AssignmentRevision,
		MissionID: session.request.MissionID, GovernanceTaskID: session.request.TaskID, WorkItemID: session.request.WorkItemID, RunID: session.request.RunID,
		GoalVersion: session.request.GoalVersion, ContextID: session.request.ContextID, ContextHash: session.request.ContextHash, LeaseID: session.assignment.LeaseID,
		LogicalActorID: session.request.LogicalActorID, RuntimeBindingRevision: session.request.RuntimeBindingRevision, AgentFunction: session.request.AgentFunction,
		EnvironmentID: session.request.EnvironmentID, AgentTeamsInstanceID: session.request.AgentTeamsInstanceID, RoomID: session.assignment.MatrixRoomID,
		SourceEventID: eventID, SourceEventType: "matrix.dispatch", SourceSystem: "matrix", Status: "dispatched",
		ArtifactRefs: []model.ArtifactRef{{Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256}},
		Artifacts:    []trace.ArtifactObservation{{Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256, EnvironmentID: artifact.EnvironmentID, Size: artifact.Size}},
		StartedAt:    session.assignment.CreatedAt, FinishedAt: session.assignment.CreatedAt,
	}
	_, err := session.transport.config.Trace.AppendIdempotent(ctx, record)
	return err
}

func (session *session) appendReceiptTrace(ctx context.Context, receipt executionreceipt.ExecutionReceipt) error {
	if session.transport.config.Trace == nil {
		return nil
	}
	record := executionreceipt.CandidateTraceEnvelope(session.assignment, receipt)
	_, err := session.transport.config.Trace.AppendIdempotent(ctx, record)
	return err
}

func (session *session) Cancel(ctx context.Context) error {
	session.once.Do(func() {
		if stopper, ok := session.transport.config.Matrix.(interface {
			Stop(context.Context, string) error
		}); ok {
			if err := stopper.Stop(ctx, session.assignment.MatrixRoomID); err != nil {
				session.cancelErr = err
				return
			}
		}
		session.cancelErr = session.transport.config.Orchestrator.StopMissionTeam(ctx, session.assignment.MissionID)
	})
	return session.cancelErr
}

var _ executor.AgentTeamsTransport = (*Transport)(nil)

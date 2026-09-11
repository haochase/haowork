package agentteamsbridge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

func TestTransportRegistersAssignmentBeforeMatrixDispatch(t *testing.T) {
	order := make([]string, 0, 7)
	assignments := &receiptAssignmentFake{order: &order}
	matrix := &receiptMatrixFake{order: &order, dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch-001"}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{order: &order}, Matrix: matrix, Artifacts: &receiptArtifactFake{order: &order},
		RuntimeBindings: &bindingFake{order: &order}, Assignments: assignments, ReceiptWaitTimeout: 10 * time.Millisecond,
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})

	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	assignments.receipt = validExecutionReceipt(assignments.registered.ID)
	for range session.Events(context.Background(), "") {
	}

	want := []string{"upload", "topology", "bind_five", "register", "send", "mark_dispatched", "wait"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("operation order = %#v, want %#v", order, want)
	}
	if assignments.registered.MissionArtifactURI == "" || assignments.registered.LeaderRuntimePrincipalID != "principal-leader" {
		t.Fatalf("registered assignment = %#v", assignments.registered)
	}
}

func TestTransportChecksRouteAfterRuntimeBindingsAndBeforeAssignment(t *testing.T) {
	order := make([]string, 0, 7)
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{order: &order},
		Matrix:       &receiptMatrixFake{order: &order, dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch-001"}},
		Artifacts:    &receiptArtifactFake{order: &order}, RuntimeBindings: &bindingFake{order: &order},
		Assignments: &receiptAssignmentFake{order: &order},
		Mission:     func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
		Ready: func(context.Context) error {
			order = append(order, "leader_consumer_ready")
			return nil
		},
	})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err != nil {
		t.Fatal(err)
	}
	want := []string{"upload", "topology", "bind_five", "leader_consumer_ready", "register", "send", "mark_dispatched"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("operation order = %#v, want %#v", order, want)
	}
}

func TestTransportMarksAssignmentDispatchedWithMatrixEventID(t *testing.T) {
	assignments := &receiptAssignmentFake{}
	matrix := &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch-001"}}
	transport := newReceiptTransport(t, matrix, assignments, &receiptArtifactFake{})

	if _, err := transport.Start(context.Background(), fullStartRequest()); err != nil {
		t.Fatal(err)
	}
	if assignments.markedAssignmentID == "" || assignments.markedEventID != "$dispatch-001" {
		t.Fatalf("MarkDispatched(%q, %q)", assignments.markedAssignmentID, assignments.markedEventID)
	}
	if len(matrix.sent) != 1 || matrix.sent[0].AssignmentID != assignments.registered.ID || len(matrix.sent[0].AllowedSkills) != 1 {
		t.Fatalf("Matrix outbound = %#v", matrix.sent)
	}
}

func TestTransportRejectsEmptyMatrixDispatchEventID(t *testing.T) {
	transport := newReceiptTransport(t, &receiptMatrixFake{}, &receiptAssignmentFake{}, &receiptArtifactFake{})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err == nil {
		t.Fatal("Start accepted an empty Matrix event_id")
	}
}

func TestTransportReturnsReceiptAsGovernedEventWithoutMatrixCorrelation(t *testing.T) {
	assignments := &receiptAssignmentFake{}
	matrix := &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}
	transport := newReceiptTransport(t, matrix, assignments, &receiptArtifactFake{})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	assignments.receipt = validExecutionReceipt(assignments.registered.ID)

	event, ok := <-session.Events(context.Background(), "ignored-matrix-cursor")
	if !ok {
		t.Fatal("receipt event was not emitted")
	}
	if event.Kind != "execution_receipt" || event.SourceEventID != assignments.receipt.ID || event.Summary != "succeeded" || event.ActorID != "principal-leader" || event.ActorRole != string(model.FunctionDeliveryLeader) {
		t.Fatalf("governed receipt event = %#v", event)
	}
	if matrix.syncCalls != 0 {
		t.Fatalf("Matrix Sync calls = %d; Matrix text must not unlock execution", matrix.syncCalls)
	}
}

func TestTransportRejectsReceiptFromDifferentAssignment(t *testing.T) {
	assignments := &receiptAssignmentFake{receipt: validExecutionReceipt("ASN-other")}
	transport := newReceiptTransport(t, &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}, assignments, &receiptArtifactFake{})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "") {
		t.Fatal("cross-assignment receipt reached the executor")
	}
	assertSessionErrorCode(t, session, "execution_report_invalid")
}

func TestTransportDownloadsAndVerifiesReceiptArtifacts(t *testing.T) {
	data := []byte("verified receipt artifact")
	assignments := &receiptAssignmentFake{}
	artifacts := &receiptArtifactFake{downloads: map[string][]byte{"evidence/report.json": data}}
	transport := newReceiptTransport(t, &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}, assignments, artifacts)
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	receipt := validExecutionReceipt(assignments.registered.ID)
	receipt.Report.Artifacts = []executionreceipt.Artifact{{Kind: "report", URI: "evidence/report.json", SHA256: artifactDigest(data), EnvironmentID: "ENV-001", Size: int64(len(data))}}
	assignments.receipt = receipt
	event := <-session.Events(context.Background(), "")
	if len(event.Artifacts) != 1 || event.Artifacts[0].URI != "evidence/report.json" {
		t.Fatalf("verified receipt artifacts = %#v", event.Artifacts)
	}

	assignments = &receiptAssignmentFake{}
	artifacts = &receiptArtifactFake{downloads: map[string][]byte{"evidence/report.json": []byte("tampered")}}
	transport = newReceiptTransport(t, &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}, assignments, artifacts)
	session, err = transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	receipt = validExecutionReceipt(assignments.registered.ID)
	receipt.Report.Artifacts = []executionreceipt.Artifact{{Kind: "report", URI: "evidence/report.json", SHA256: artifactDigest(data), EnvironmentID: "ENV-001", Size: int64(len(data))}}
	assignments.receipt = receipt
	for range session.Events(context.Background(), "") {
		t.Fatal("tampered artifact reached the executor")
	}
	if failure := <-session.(interface {
		Errors(context.Context) <-chan error
	}).Errors(context.Background()); !errors.Is(failure, agentteamsbridge.ErrArtifactDigest) {
		t.Fatalf("artifact failure = %v, want ErrArtifactDigest", failure)
	}
}

func TestProductionS3ReceiptSubmitWaitDownloadRoundTrip(t *testing.T) {
	root := t.TempDir()
	client := &memoryS3Client{objects: map[string][]byte{}}
	artifacts, err := agentteamsbridge.NewS3ArtifactStoreWithClient(agentteamsbridge.S3ArtifactStoreConfig{
		Bucket: "haowork-public-artifacts", EnvironmentID: "ENV-001", MissionID: "MSN-001", MaxBytes: 1024,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := executionreceipt.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ledger := trace.New(root)
	matrix := &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{}, Matrix: matrix, Artifacts: artifacts, RuntimeBindings: &bindingFake{},
		Assignments: assignments, Trace: ledger, ReceiptWaitTimeout: time.Second,
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.sent) != 1 {
		t.Fatalf("Matrix sends=%d", len(matrix.sent))
	}
	assignment, err := assignments.Assignment(context.Background(), matrix.sent[0].AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(assignment.MissionArtifactURI, "s3://haowork-public-artifacts/environments/ENV-001/missions/MSN-001/artifacts/") {
		t.Fatalf("mission artifact URI=%q", assignment.MissionArtifactURI)
	}

	data := []byte("verified production receipt artifact")
	digest := artifactDigest(data)
	key, err := artifacts.Upload(context.Background(), "reports/result.json", data, digest)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := artifacts.ArtifactURI(key)
	if err != nil {
		t.Fatal(err)
	}
	service := executionreceipt.Service{Store: assignments, Trace: ledger, Clock: func() time.Time { return time.Now().UTC() }}
	_, err = service.Submit(context.Background(), executionreceipt.Principal{
		LogicalActorID: assignment.LeaderLogicalActorID, RuntimePrincipalID: assignment.LeaderRuntimePrincipalID,
		EnvironmentID: assignment.EnvironmentID, AgentTeamsInstanceID: assignment.AgentTeamsInstanceID,
		BindingRevision: assignment.RuntimeBindingRevision, Function: model.FunctionDeliveryLeader,
	}, executionreceipt.ExecutionReport{
		AssignmentID: assignment.ID, Status: "succeeded", SummarySHA256: strings.Repeat("b", 64),
		Artifacts: []executionreceipt.Artifact{{Kind: "report", URI: uri, SHA256: digest, EnvironmentID: assignment.EnvironmentID, Size: int64(len(data))}},
		Checks:    []executionreceipt.Check{{Name: "unit-tests", Status: "passed", EvidenceSHA256: strings.Repeat("c", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	event, ok := <-session.Events(context.Background(), "")
	if !ok {
		var failure error
		select {
		case failure = <-session.(interface {
			Errors(context.Context) <-chan error
		}).Errors(context.Background()):
		default:
		}
		t.Fatalf("receipt event was not emitted: %v", failure)
	}
	if len(event.Artifacts) != 1 || event.Artifacts[0].URI != uri || event.Artifacts[0].SHA256 != digest {
		t.Fatalf("receipt artifacts=%#v", event.Artifacts)
	}
	if client.lastBucket != "haowork-public-artifacts" || client.lastKey != key || client.statCalls == 0 || client.getCalls == 0 {
		t.Fatalf("S3 download bucket/key=%q/%q stats=%d gets=%d", client.lastBucket, client.lastKey, client.statCalls, client.getCalls)
	}
}

func TestTransportReturnsExecutionReportMissingOnTimeout(t *testing.T) {
	assignments := &receiptAssignmentFake{waitErr: context.DeadlineExceeded}
	transport := newReceiptTransport(t, &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}, assignments, &receiptArtifactFake{})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "") {
		t.Fatal("timeout emitted a governed event")
	}
	assertSessionErrorCode(t, session, "execution_report_missing")
}

func TestTransportRecordsMatrixDispatchAndReceiptSeparately(t *testing.T) {
	assignments := &receiptAssignmentFake{}
	ledger := trace.New(t.TempDir())
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{}, Matrix: &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}},
		Artifacts: &receiptArtifactFake{}, RuntimeBindings: &bindingFake{}, Assignments: assignments, Trace: ledger,
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	assignments.receipt = validExecutionReceipt(assignments.registered.ID)
	for range session.Events(context.Background(), "") {
	}
	records, err := ledger.ReadAll(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("trace records = %#v, err=%v", records, err)
	}
	if records[0].SourceSystem != "matrix" || records[0].SourceEventID != "$dispatch" || records[0].Candidate || records[1].SourceEventType != "execution.report.submitted" || records[1].SourceEventID != "ERC-001" || !records[1].Candidate || records[1].AssignmentID != assignments.registered.ID {
		t.Fatalf("dispatch/receipt traces = %#v", records)
	}
}

func TestTransportRestartReusesSubmittedReceiptWithoutDuplicateDispatchOrTrace(t *testing.T) {
	assignments := &recoveringAssignmentFake{}
	matrix := &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}}
	ledger := trace.New(t.TempDir())
	mission := receiptTestMission()
	config := agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{}, Matrix: matrix, Artifacts: &receiptArtifactFake{}, RuntimeBindings: &bindingFake{},
		Assignments: assignments, Trace: ledger, Mission: func(string) (model.MissionEnvelope, error) { return mission, nil },
	}
	first := agentteamsbridge.NewTransport(config)
	session, err := first.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	assignments.receipt = validExecutionReceipt(assignments.assignment.ID)
	assignments.assignment.State = executionreceipt.AssignmentReportSubmitted
	for range session.Events(context.Background(), "") {
	}

	restarted := agentteamsbridge.NewTransport(config)
	session, err = restarted.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for range session.Events(context.Background(), "") {
	}
	records, err := ledger.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if matrixSendCount(matrix) != 1 || len(records) != 2 {
		t.Fatalf("restart matrix sends=%d traces=%#v", matrixSendCount(matrix), records)
	}
}

func TestTransportPersistsReadyTopologyBindingsBeforeAssignment(t *testing.T) {
	binder := &bindingFake{}
	assignments := &receiptAssignmentFake{}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{}, Matrix: &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}},
		Artifacts: &receiptArtifactFake{}, RuntimeBindings: binder, Assignments: assignments,
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})
	session, err := transport.Start(context.Background(), fullStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	bound := session.(executor.BoundAgentTeamsSession).BoundRequest()
	if bound.RuntimeBindingRevision != 2 || len(binder.bindings) != 5 || assignments.registered.RuntimeBindingRevision != 2 {
		t.Fatalf("bound request=%#v bindings=%#v assignment=%#v", bound, binder.bindings, assignments.registered)
	}
}

func TestTransportNeverFallsBackToFakeExecutor(t *testing.T) {
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err == nil {
		t.Fatal("Start succeeded without real bridge dependencies")
	}
}

func TestTransportRejectsTopologyThatDoesNotMatchBoundManagerPrincipal(t *testing.T) {
	bad := &topologyFake{managerPrincipalID: "forged"}
	transport := agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: bad, Matrix: &receiptMatrixFake{dispatch: agentteamsbridge.MatrixDispatch{EventID: "$dispatch"}},
		Artifacts: &receiptArtifactFake{}, RuntimeBindings: &bindingFake{}, Assignments: &receiptAssignmentFake{},
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})
	if _, err := transport.Start(context.Background(), fullStartRequest()); err == nil {
		t.Fatal("Start accepted forged Manager topology")
	}
}

func assertSessionErrorCode(t *testing.T, session executor.AgentTeamsSession, want string) {
	t.Helper()
	failure := <-session.(interface {
		Errors(context.Context) <-chan error
	}).Errors(context.Background())
	coded, ok := failure.(interface{ SafeCode() string })
	if !ok || coded.SafeCode() != want {
		t.Fatalf("session error = %#v, want %q", failure, want)
	}
}

func newReceiptTransport(t *testing.T, matrix agentteamsbridge.MatrixClient, assignments agentteamsbridge.AssignmentStore, artifacts agentteamsbridge.ArtifactStore) *agentteamsbridge.Transport {
	t.Helper()
	return agentteamsbridge.NewTransport(agentteamsbridge.TransportConfig{
		Orchestrator: &topologyFake{}, Matrix: matrix, Artifacts: artifacts, Assignments: assignments,
		RuntimeBindings: &bindingFake{}, ReceiptWaitTimeout: 10 * time.Millisecond,
		Mission: func(string) (model.MissionEnvelope, error) { return receiptTestMission(), nil },
	})
}

type receiptMatrixFake struct {
	order     *[]string
	dispatch  agentteamsbridge.MatrixDispatch
	sent      []agentteamsbridge.MatrixOutbound
	syncCalls int
}

func (fake *receiptMatrixFake) Send(_ context.Context, _ string, outbound agentteamsbridge.MatrixOutbound) (agentteamsbridge.MatrixDispatch, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "send")
	}
	fake.sent = append(fake.sent, outbound)
	return fake.dispatch, nil
}

func (fake *receiptMatrixFake) Sync(context.Context, string) (agentteamsbridge.MatrixPage, error) {
	fake.syncCalls++
	return agentteamsbridge.MatrixPage{}, nil
}

type receiptAssignmentFake struct {
	order              *[]string
	registered         executionreceipt.ExecutionAssignment
	markedAssignmentID string
	markedEventID      string
	receipt            executionreceipt.ExecutionReceipt
	waitErr            error
}

type recoveringAssignmentFake struct {
	assignment executionreceipt.ExecutionAssignment
	receipt    executionreceipt.ExecutionReceipt
}

func (fake *recoveringAssignmentFake) Register(_ context.Context, assignment executionreceipt.ExecutionAssignment) (executionreceipt.ExecutionAssignment, error) {
	if fake.assignment.ID != "" {
		return executionreceipt.ExecutionAssignment{}, executionreceipt.ErrAssignmentConflict
	}
	fake.assignment = assignment
	return assignment, nil
}

func (fake *recoveringAssignmentFake) Assignment(context.Context, string) (executionreceipt.ExecutionAssignment, error) {
	return fake.assignment, nil
}

func (fake *recoveringAssignmentFake) MarkDispatched(_ context.Context, _, eventID string) (executionreceipt.ExecutionAssignment, error) {
	fake.assignment.State = executionreceipt.AssignmentDispatched
	fake.assignment.MatrixDispatchEventID = eventID
	return fake.assignment, nil
}

func (fake *recoveringAssignmentFake) Wait(context.Context, string, time.Duration) (executionreceipt.ExecutionReceipt, error) {
	return fake.receipt, nil
}

func matrixSendCount(fake *receiptMatrixFake) int { return len(fake.sent) }

func (fake *receiptAssignmentFake) Register(_ context.Context, assignment executionreceipt.ExecutionAssignment) (executionreceipt.ExecutionAssignment, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "register")
	}
	fake.registered = assignment
	return assignment, nil
}

func (fake *receiptAssignmentFake) MarkDispatched(_ context.Context, assignmentID, eventID string) (executionreceipt.ExecutionAssignment, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "mark_dispatched")
	}
	fake.markedAssignmentID, fake.markedEventID = assignmentID, eventID
	assignment := fake.registered
	assignment.State = executionreceipt.AssignmentDispatched
	assignment.MatrixDispatchEventID = eventID
	return assignment, nil
}

func (fake *receiptAssignmentFake) Wait(_ context.Context, _ string, _ time.Duration) (executionreceipt.ExecutionReceipt, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "wait")
	}
	return fake.receipt, fake.waitErr
}

type receiptArtifactFake struct {
	order     *[]string
	downloads map[string][]byte
}

func (fake *receiptArtifactFake) Upload(context.Context, string, []byte, string) (string, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "upload")
	}
	return "environments/ENV-001/missions/MSN-001/artifacts/mission", nil
}

func (fake *receiptArtifactFake) Download(_ context.Context, ref string) ([]byte, error) {
	return append([]byte(nil), fake.downloads[ref]...), nil
}

type topologyFake struct {
	order              *[]string
	managerPrincipalID string
}

func (fake *topologyFake) EnsureMissionTeam(context.Context, model.MissionEnvelope) (agentteamsbridge.RuntimeTopology, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "topology")
	}
	manager := fake.managerPrincipalID
	if manager == "" {
		manager = "principal-manager"
	}
	return agentteamsbridge.RuntimeTopology{
		MissionID: "MSN-001", TeamName: "team", ManagerPrincipalID: manager, LeaderPrincipalID: "principal-leader",
		WorkerPrincipalIDs: map[model.AgentFunction]string{model.FunctionResearch: "principal-research", model.FunctionBuild: "principal-build", model.FunctionVerify: "principal-verify"},
		HumanPrincipalID:   "principal-human", ManagerRoomID: "!manager", LeaderRoomID: "!leader", TeamRoomID: "!team",
	}, nil
}

func (*topologyFake) StopMissionTeam(context.Context, string) error { return nil }

type bindingFake struct {
	order    *[]string
	bindings []model.RuntimeBinding
}

func (fake *bindingFake) BindRuntimeTopology(_ context.Context, bindings []model.RuntimeBinding, _ model.Actor) ([]model.RuntimeBinding, error) {
	if fake.order != nil {
		*fake.order = append(*fake.order, "bind_five")
	}
	fake.bindings = append([]model.RuntimeBinding(nil), bindings...)
	result := append([]model.RuntimeBinding(nil), bindings...)
	for index := range result {
		result[index].Revision = 2
		result[index].Status = "active"
	}
	return result, nil
}

func validExecutionReceipt(assignmentID string) executionreceipt.ExecutionReceipt {
	return executionreceipt.ExecutionReceipt{
		ID: "ERC-001", AssignmentID: assignmentID, ReportSHA256: strings.Repeat("c", 64), SubmittedAt: time.Now().UTC(),
		Report: executionreceipt.ExecutionReport{AssignmentID: assignmentID, Status: "succeeded", SummarySHA256: strings.Repeat("b", 64)},
	}
}

func fullStartRequest() executor.AgentTeamsStartRequest {
	return executor.AgentTeamsStartRequest{
		RunID: "RUN-001", TaskID: "TSK-001", StepID: "STEP-001", MissionID: "MSN-001", WorkItemID: "WI-1",
		GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash", LogicalActorID: "AGT-MANAGER",
		RuntimePrincipalID: "principal-manager", RuntimeBindingRevision: 2, AgentFunction: model.FunctionManager,
		EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001",
	}
}

func receiptTestMission() model.MissionEnvelope {
	now := time.Now().UTC().Add(-time.Minute)
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: "MSN-001", ProjectID: "PRJ-001", ContextID: "CTX-001", ContextHash: "hash", LeaseID: "LSE-001", PolicyVersion: "POL-1", GoalVersion: 1,
		GovernanceTaskIDs: []string{"TSK-001"}, CompletionCriteria: []string{"verified"}, AllowedScopes: []string{"internal/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "build", Version: "1"}},
		RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-MANAGER", model.FunctionDeliveryLeader: "AGT-LEADER", model.FunctionResearch: "AGT-RESEARCH", model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"},
		RiskLevel:       "L2", EnvironmentID: "ENV-001", IssuedAt: now, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		panic(err)
	}
	return mission
}

func artifactDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

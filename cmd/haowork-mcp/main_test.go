package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/executionreceipt"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

func TestCoreStateBindingReaderAcceptsOnlyCurrentCoreWorkItem(t *testing.T) {
	state := model.ProjectState{
		Goal:  model.GoalVersion{Version: 1},
		Tasks: map[string]model.Task{"TSK-001": {ID: "TSK-001", GoalVersion: 1}},
		Runs: map[string]model.Run{"RUN-001": {
			ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "context-hash", Status: model.StatusRunning,
		}},
		Missions: map[string]model.MissionEnvelope{"MSN-001": {
			ID: "MSN-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "context-hash", EnvironmentID: "public",
			GovernanceTaskIDs: []string{"TSK-001"}, RoleAssignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD"},
		}},
		Agents: map[string]model.LogicalAgent{"AGT-BUILD": {
			ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild, Status: "active",
		}},
		RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-BUILD": {{
			LogicalActorID: "AGT-BUILD", Revision: 2, EnvironmentID: "public", RuntimePrincipalID: "runtime-build", AgentTeamsInstanceID: "AT-001", Status: "active",
		}}},
	}
	reader := coreStateBindingReader{state: skillruntime.StaticState(state)}
	invocation := skillruntime.Invocation{
		MissionID: "MSN-001", TaskID: "TSK-001", WorkItemID: "TSK-001", RunID: "RUN-001", TraceID: "TRC-001", GoalVersion: 1,
		LogicalActorID: "AGT-BUILD", RuntimePrincipalID: "runtime-build", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001", RuntimeBindingRevision: 2,
		ContextID: "CTX-001", ContextHash: "context-hash",
	}
	if err := reader.ValidateInvocation(context.Background(), invocation); err != nil {
		t.Fatalf("current Core invocation rejected: %v", err)
	}
	invocation.WorkItemID = "WI-FORGED"
	if err := reader.ValidateInvocation(context.Background(), invocation); err == nil {
		t.Fatal("binding reader accepted a work item not registered by the current Core run")
	}
}

func TestCoreStateBindingReaderValidatesReceiptWithoutRunProjection(t *testing.T) {
	store, err := executionreceipt.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assignment := executionreceipt.ExecutionAssignment{
		ID: "ASN-46fc8933c009dd44765c6f4b01aeaf9d", ProjectID: "PRJ-001", MissionID: "MSN-001", GovernanceTaskID: "TSK-001",
		RunID: "RUN-001", WorkItemID: "WKI-001", EnvironmentID: "public", AgentTeamsInstanceID: "AT-001",
		LeaderLogicalActorID: "AGT-LEADER", LeaderRuntimePrincipalID: "runtime-leader", AssignmentRevision: 1,
		RuntimeBindingRevision: 2, GoalVersion: 1, ContextID: "CTX-001", ContextHash: strings.Repeat("c", 64), LeaseID: "LSE-001",
		MissionArtifactURI: "s3://haowork/missions/MSN-001/mission.json", MissionArtifactSHA256: strings.Repeat("d", 64),
		MatrixRoomID: "!leader:example.test", State: executionreceipt.AssignmentPrepared,
		CreatedAt: time.Now().UTC().Add(-time.Minute), Deadline: time.Now().UTC().Add(time.Hour),
	}
	if _, err := store.Register(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDispatched(context.Background(), assignment.ID, "$dispatch"); err != nil {
		t.Fatal(err)
	}
	reader := coreStateBindingReader{receipts: store}
	invocation := skillruntime.Invocation{
		MissionID: assignment.MissionID, TaskID: assignment.GovernanceTaskID, WorkItemID: assignment.WorkItemID,
		RunID: assignment.RunID, LogicalActorID: assignment.LeaderLogicalActorID, RuntimePrincipalID: assignment.LeaderRuntimePrincipalID,
		AgentTeamsInstanceID: assignment.AgentTeamsInstanceID, RuntimeBindingRevision: assignment.RuntimeBindingRevision,
		SkillName: "submit_execution_report", EnvironmentID: assignment.EnvironmentID, GoalVersion: assignment.GoalVersion,
		ContextID: assignment.ContextID, ContextHash: assignment.ContextHash, LeaseID: assignment.LeaseID,
		Input: []byte(`{"assignment_id":"ASN-46fc8933c009dd44765c6f4b01aeaf9d"}`),
	}
	if err := reader.ValidateInvocation(context.Background(), invocation); err != nil {
		t.Fatalf("receipt invocation rejected without state.Runs: %v", err)
	}
	invocation.RuntimeBindingRevision++
	if err := reader.ValidateInvocation(context.Background(), invocation); !errors.Is(err, executionreceipt.ErrBindingRevisionMismatch) {
		t.Fatalf("receipt binding error = %v, want ErrBindingRevisionMismatch", err)
	}
}

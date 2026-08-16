package main

import (
	"context"
	"testing"

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

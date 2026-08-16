package app

import (
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestResolveMissionManagerBindingRejectsMissingAmbiguousOrStaleMission(t *testing.T) {
	run := model.Run{ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash"}
	for _, test := range []struct {
		name  string
		state model.ProjectState
		want  string
	}{
		{name: "missing", state: model.ProjectState{Missions: map[string]model.MissionEnvelope{}}, want: "exactly one"},
		{name: "ambiguous", state: model.ProjectState{Missions: map[string]model.MissionEnvelope{"one": bindingMission("MSN-1"), "two": bindingMission("MSN-2")}}, want: "exactly one"},
		{name: "stale", state: model.ProjectState{Missions: map[string]model.MissionEnvelope{"one": func() model.MissionEnvelope {
			mission := bindingMission("MSN-1")
			mission.ContextHash = "old"
			return mission
		}()}, RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-MANAGER": {{LogicalActorID: "AGT-MANAGER", Revision: 1, EnvironmentID: "ENV-001", Status: "active"}}}}, want: "stale"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveMissionManagerBinding(test.state, run); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolve error = %v", err)
			}
		})
	}
}

func TestResolveMissionManagerBindingUsesCurrentManagerRuntimeRevision(t *testing.T) {
	state := model.ProjectState{Missions: map[string]model.MissionEnvelope{"mission": bindingMission("MSN-001")}, RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-MANAGER": {{LogicalActorID: "AGT-MANAGER", Revision: 1, EnvironmentID: "ENV-001", Status: "inactive"}, {LogicalActorID: "AGT-MANAGER", Revision: 2, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "principal", Status: "active"}}}}
	binding, err := resolveMissionManagerBinding(state, model.Run{ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash"})
	if err != nil || binding.managerID != "AGT-MANAGER" || binding.runtime.Revision != 2 {
		t.Fatalf("binding = %#v, err = %v", binding, err)
	}
}

func TestResolveMissionManagerBindingAllowsMissionWithMultipleTasksWhenRunMembershipIsUnique(t *testing.T) {
	mission := bindingMission("MSN-001")
	mission.GovernanceTaskIDs = []string{"TSK-OTHER", "TSK-001"}
	state := model.ProjectState{Missions: map[string]model.MissionEnvelope{"mission": mission}, RuntimeBindings: map[string][]model.RuntimeBinding{"AGT-MANAGER": {{LogicalActorID: "AGT-MANAGER", Revision: 2, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", Status: "active"}}}}
	if _, err := resolveMissionManagerBinding(state, model.Run{ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash"}); err != nil {
		t.Fatalf("multi-task mission binding = %v", err)
	}
}

func bindingMission(id string) model.MissionEnvelope {
	return model.MissionEnvelope{ID: id, GoalVersion: 1, ContextID: "CTX-001", ContextHash: "hash", EnvironmentID: "ENV-001", GovernanceTaskIDs: []string{"TSK-001"}, RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-MANAGER"}}
}

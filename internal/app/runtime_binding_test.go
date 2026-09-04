package app

import (
	"context"
	"testing"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestBindRuntimeTopologyPersistsReplayableManagerAndWorkerBindings(t *testing.T) {
	service, repository, _ := prepareGovernedBridgeRunningRun(t)
	bindings, err := service.BindRuntimeTopology(context.Background(), []model.RuntimeBinding{
		{LogicalActorID: "AGT-MANAGER", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "manager-ready", HumanPrincipalID: "human-ready", LeaderRoomID: "!leader", TeamRoomID: "!team"},
		{LogicalActorID: "AGT-LEADER", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "leader-ready", HumanPrincipalID: "human-ready", LeaderRoomID: "!leader", TeamRoomID: "!team"},
		{LogicalActorID: "AGT-RESEARCH", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "research-ready", HumanPrincipalID: "human-ready", TeamRoomID: "!team"},
		{LogicalActorID: "AGT-BUILD", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "build-ready", HumanPrincipalID: "human-ready", TeamRoomID: "!team"},
		{LogicalActorID: "AGT-VERIFY", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "verify-ready", HumanPrincipalID: "human-ready", TeamRoomID: "!team"},
	}, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 5 || bindings[0].Revision != 2 || bindings[3].Revision != 1 {
		t.Fatalf("persisted bindings = %#v", bindings)
	}

	replayed := New("PRJ-TEST", 1, repository, &testkit.IDs{}, testkit.Clock{Value: testTime})
	state, err := replayed.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	manager := state.RuntimeBindings["AGT-MANAGER"]
	build := state.RuntimeBindings["AGT-BUILD"]
	if len(manager) != 2 || manager[1].Revision != 2 || manager[1].RuntimePrincipalID != "manager-ready" || manager[1].HumanPrincipalID != "human-ready" || manager[1].LeaderRoomID != "!leader" || manager[1].TeamRoomID != "!team" {
		t.Fatalf("replayed manager binding = %#v", manager)
	}
	if len(build) != 1 || build[0].Revision != 1 || build[0].RuntimePrincipalID != "build-ready" || build[0].HumanPrincipalID != "human-ready" || build[0].TeamRoomID != "!team" {
		t.Fatalf("replayed worker binding = %#v", build)
	}
}

func TestBindRuntimeTopologyRequiresHumanOwner(t *testing.T) {
	service, _, _ := prepareGovernedBridgeRunningRun(t)
	_, err := service.BindRuntimeTopology(context.Background(), []model.RuntimeBinding{{
		LogicalActorID: "AGT-MANAGER", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "manager-ready",
	}}, agent("AGT-001"))
	if err != ErrApprovalRequired {
		t.Fatalf("BindRuntimeTopology() error = %v, want ErrApprovalRequired", err)
	}
}

func TestBindRuntimeTopologyExactRetryKeepsExistingRevisions(t *testing.T) {
	service, repository, _ := prepareGovernedBridgeRunningRun(t)
	input := []model.RuntimeBinding{{
		LogicalActorID: "AGT-BUILD", EnvironmentID: "ENV-001", AgentTeamsInstanceID: "AT-001", RuntimePrincipalID: "build-ready",
		HumanPrincipalID: "human-ready", TeamRoomID: "!team",
	}}
	first, err := service.BindRuntimeTopology(context.Background(), input, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.BindRuntimeTopology(context.Background(), input, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Revision != 1 || second[0].Revision != first[0].Revision || len(after) != len(before) {
		t.Fatalf("exact runtime-binding retry advanced state: first=%#v second=%#v events=%d -> %d", first, second, len(before), len(after))
	}
}

func TestBootstrapRuntimeTopologyRegistersIdentityAndBindingAtomically(t *testing.T) {
	service, repository := newWorkflowService(t)
	agents := []model.LogicalAgent{{ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild}}
	bindings := []model.RuntimeBinding{{LogicalActorID: "AGT-BUILD", EnvironmentID: "internal", AgentTeamsInstanceID: "default", RuntimePrincipalID: "runtime-internal-build"}}

	first, err := service.BootstrapRuntimeTopology(context.Background(), agents, bindings, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.BootstrapRuntimeTopology(context.Background(), agents, bindings, owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := repository.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Revision != 1 || len(second) != 1 || second[0] != first[0] || len(before) != 3 || len(after) != len(before) {
		t.Fatalf("bootstrap result first=%#v second=%#v events=%d->%d", first, second, len(before), len(after))
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Agents["AGT-BUILD"].Function != model.FunctionBuild || state.RuntimeBindings["AGT-BUILD"][0].RuntimePrincipalID != "runtime-internal-build" {
		t.Fatalf("bootstrapped state = %#v, %#v", state.Agents, state.RuntimeBindings)
	}
}

func TestBootstrapRuntimeTopologyRejectsNonOwnerWithZeroWrites(t *testing.T) {
	service, repository := newWorkflowService(t)
	agents := []model.LogicalAgent{{ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild}}
	bindings := []model.RuntimeBinding{{LogicalActorID: "AGT-BUILD", EnvironmentID: "internal", AgentTeamsInstanceID: "default", RuntimePrincipalID: "runtime-internal-build"}}
	before, _ := repository.ReadAll(context.Background())

	if _, err := service.BootstrapRuntimeTopology(context.Background(), agents, bindings, agent("AGT-OTHER")); err != ErrApprovalRequired {
		t.Fatalf("BootstrapRuntimeTopology() error = %v, want ErrApprovalRequired", err)
	}
	after, _ := repository.ReadAll(context.Background())
	if len(after) != len(before) {
		t.Fatalf("non-owner bootstrap wrote %d event(s)", len(after)-len(before))
	}
}

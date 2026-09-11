package corebridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

func TestRecordRunPersistFailureDoesNotPublishInMemoryRun(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state.persistDocument = func(persistedState) error { return errors.New("injected persist failure") }
	request := executor.AgentTeamsStartRequest{RunID: "RUN-FAIL", RuntimeBindingRevision: 1}
	receiptID := "ERC-" + strings.Repeat("a", 64)
	if err := state.RecordRun(request, []string{receiptID}, []string{receiptID}, "cursor-one"); err == nil || !strings.Contains(err.Error(), "injected persist failure") {
		t.Fatalf("RecordRun() error=%v", err)
	}
	if got, exists := state.RunRequest(request.RunID); exists {
		t.Fatalf("failed run became visible in memory: %#v", got)
	}
}

func TestRecordRunRejectsNonCanonicalReceiptIDs(t *testing.T) {
	invalid := []string{"ERC-", "ERC-forged", "ERC-" + strings.Repeat("A", 64), "ERC-" + strings.Repeat("a", 63), "ERC-" + strings.Repeat("a", 65)}
	for _, receiptID := range invalid {
		state, err := OpenState(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		request := executor.AgentTeamsStartRequest{RunID: "RUN-INVALID", RuntimeBindingRevision: 1}
		if err := state.RecordRun(request, []string{receiptID}, []string{receiptID}, "cursor"); err == nil {
			t.Fatalf("RecordRun accepted receipt ID %q", receiptID)
		}
	}
}

func TestRecordRunPersistFailureCannotMutateNestedMissionState(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mission := model.MissionEnvelope{
		ID: "MSN-DEEP", GovernanceTaskIDs: []string{"TASK-ORIGINAL"}, CompletionCriteria: []string{"original criterion"},
		AllowedScopes: []string{"original/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "build", Version: "1"}},
		RoleAssignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-ORIGINAL"},
	}
	state.document.Missions[mission.ID] = mission
	state.persistDocument = func(candidate persistedState) error {
		mutated := candidate.Missions[mission.ID]
		mutated.GovernanceTaskIDs[0] = "TASK-MUTATED"
		mutated.CompletionCriteria[0] = "mutated criterion"
		mutated.AllowedScopes[0] = "mutated/**"
		mutated.AllowedSkills[0].Name = "mutated"
		mutated.RoleAssignments[model.FunctionBuild] = "AGT-MUTATED"
		return errors.New("injected persist failure")
	}
	request := executor.AgentTeamsStartRequest{RunID: "RUN-DEEP", RuntimeBindingRevision: 1}
	receiptID := "ERC-" + strings.Repeat("a", 64)
	if err := state.RecordRun(request, []string{receiptID}, []string{receiptID}, "cursor"); err == nil {
		t.Fatal("RecordRun unexpectedly persisted")
	}
	stored := state.document.Missions[mission.ID]
	if stored.GovernanceTaskIDs[0] != "TASK-ORIGINAL" || stored.CompletionCriteria[0] != "original criterion" || stored.AllowedScopes[0] != "original/**" || stored.AllowedSkills[0].Name != "build" || stored.RoleAssignments[model.FunctionBuild] != "AGT-ORIGINAL" {
		t.Fatalf("failed candidate mutated stored Mission: %#v", stored)
	}
}

func TestRegisterMissionGovernanceFailureDoesNotPublishLiveState(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.InitializeProject(context.Background(), "PRJ-OTHER"); err != nil {
		t.Fatal(err)
	}
	mission := atomicTestMission(t)
	if err := state.RegisterMission(context.Background(), mission); err == nil {
		t.Fatal("RegisterMission accepted a conflicting governance project")
	}
	if _, err := state.Mission(mission.ID); err == nil {
		t.Fatal("governance-failed Mission became visible")
	}
	projection, err := state.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := projection.Missions[mission.ID]; exists {
		t.Fatal("governance-failed Mission entered State projection")
	}
}

func TestRegisterMissionPersistFailureIsInvisibleAndRetryable(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mission := atomicTestMission(t)
	state.persistDocument = func(candidate persistedState) error {
		mutated := candidate.Missions[mission.ID]
		mutated.AllowedScopes[0] = "mutated/**"
		mutated.RoleAssignments[model.FunctionManager] = "AGT-MUTATED"
		return errors.New("injected Mission persist failure")
	}
	if err := state.RegisterMission(context.Background(), mission); err == nil {
		t.Fatal("RegisterMission unexpectedly persisted")
	}
	if _, err := state.Mission(mission.ID); err == nil {
		t.Fatal("persist-failed Mission became visible")
	}
	projection, err := state.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := projection.Missions[mission.ID]; exists {
		t.Fatal("persist-failed Mission entered State projection")
	}
	state.persistDocument = state.writeDocument
	if err := state.RegisterMission(context.Background(), mission); err != nil {
		t.Fatalf("retry RegisterMission() error=%v", err)
	}
	stored, err := state.Mission(mission.ID)
	if err != nil || stored.Hash != mission.Hash || stored.AllowedScopes[0] != mission.AllowedScopes[0] || stored.RoleAssignments[model.FunctionManager] != mission.RoleAssignments[model.FunctionManager] {
		t.Fatalf("retried Mission=%#v err=%v", stored, err)
	}
}

func TestRegisterMissionPersisterCannotRetainLiveAliases(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mission := atomicTestMission(t)
	var retained persistedState
	state.persistDocument = func(candidate persistedState) error {
		retained = candidate
		return state.writeDocument(candidate)
	}
	if err := state.RegisterMission(context.Background(), mission); err != nil {
		t.Fatal(err)
	}
	retainedMission := retained.Missions[mission.ID]
	retainedMission.AllowedScopes[0] = "retained/**"
	retainedMission.RoleAssignments[model.FunctionManager] = "AGT-RETAINED"
	stored, err := state.Mission(mission.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AllowedScopes[0] != mission.AllowedScopes[0] || stored.RoleAssignments[model.FunctionManager] != mission.RoleAssignments[model.FunctionManager] {
		t.Fatalf("persister retained live aliases: %#v", stored)
	}
}

func atomicTestMission(t *testing.T) model.MissionEnvelope {
	t.Helper()
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: "MSN-ATOMIC", ProjectID: "PRJ-ATOMIC", ContextID: "CTX-ATOMIC", ContextHash: "context", LeaseID: "LSE-ATOMIC", PolicyVersion: "P1", GoalVersion: 1,
		GovernanceTaskIDs: []string{"TASK-ATOMIC"}, CompletionCriteria: []string{"receipt evidence"}, AllowedScopes: []string{"src/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "submit_execution_report", Version: "1"}},
		RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-MANAGER", model.FunctionDeliveryLeader: "AGT-LEADER", model.FunctionResearch: "AGT-RESEARCH", model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"},
		RiskLevel:       "L1", EnvironmentID: "public", IssuedAt: now, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mission
}

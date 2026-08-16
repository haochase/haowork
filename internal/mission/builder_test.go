package mission

import (
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestBuildMissionIsDeterministicAcrossMapIteration(t *testing.T) {
	firstInput := validBuildInput()
	firstInput.CompletionCriteria = []string{"b", "a", "a"}
	firstInput.AllowedScopes = []string{"src", "cmd", "src"}
	firstInput.Skills = []SkillGrant{{Name: "verify", Version: "1"}, {Name: "build", Version: "2"}, {Name: "verify", Version: "1"}}
	first, err := Build(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	secondAssignments := make(map[model.AgentFunction]string)
	secondAssignments[model.FunctionVerify] = "AGT-VERIFY"
	secondAssignments[model.FunctionBuild] = "AGT-BUILD"
	secondInput := validBuildInput()
	secondInput.CompletionCriteria = []string{"a", "b"}
	secondInput.AllowedScopes = []string{"cmd", "src"}
	secondInput.Skills = []SkillGrant{{Name: "build", Version: "2"}, {Name: "verify", Version: "1"}}
	secondInput.Assignments = secondAssignments
	second, err := Build(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CanonicalJSON()) != string(second.CanonicalJSON()) || first.Hash != second.Hash {
		t.Fatalf("canonical missions differ:\n%s\n%s", first.CanonicalJSON(), second.CanonicalJSON())
	}
}

func TestBuildMissionRejectsR8BindingsAndBlankContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BuildInput)
	}{
		{name: "multiple tasks", mutate: func(input *BuildInput) { input.TaskIDs = []string{"TSK-001", "TSK-002"} }},
		{name: "lease task mismatch", mutate: func(input *BuildInput) { input.Lease.TaskID = "TSK-002" }},
		{name: "lease subject kind", mutate: func(input *BuildInput) { input.Lease.SubjectKind = "task" }},
		{name: "lease subject id", mutate: func(input *BuildInput) { input.Lease.SubjectID = "AGT-OTHER" }},
		{name: "scope outside lease", mutate: func(input *BuildInput) { input.AllowedScopes = []string{"other"} }},
		{name: "skill outside lease", mutate: func(input *BuildInput) { input.Skills = []SkillGrant{{Name: "deploy", Version: "1"}} }},
		{name: "blank task", mutate: func(input *BuildInput) { input.TaskIDs = []string{""} }},
		{name: "blank assignment", mutate: func(input *BuildInput) { input.Assignments[model.FunctionManager] = "" }},
		{name: "blank scope", mutate: func(input *BuildInput) { input.AllowedScopes = []string{""} }},
		{name: "blank criterion", mutate: func(input *BuildInput) { input.CompletionCriteria = []string{""} }},
		{name: "blank skill", mutate: func(input *BuildInput) { input.Skills = []SkillGrant{{Name: "", Version: "1"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validBuildInput()
			test.mutate(&input)
			if _, err := Build(input); err == nil {
				t.Fatal("Build() succeeded, want R8 contract rejection")
			}
		})
	}
}

func TestBuildMissionRejectsStaleContextOrLease(t *testing.T) {
	_, err := Build(BuildInput{ID: "MSN-001", ProjectID: "PRJ-TEST", Context: model.ContextSlice{ID: "CTX-001", SliceHash: "context-hash", GoalVersion: 0}, GoalVersion: 1, Lease: model.Lease{ID: "LSE-001", GoalVersion: 1, Status: "active"}, TaskIDs: []string{"TSK-001"}, CompletionCriteria: []string{"done"}, AllowedScopes: []string{"src"}, Skills: []SkillGrant{{Name: "build", Version: "1"}}, Assignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"}, RiskLevel: "L2", EnvironmentID: "ENV-001", PolicyVersion: "POL-1", IssuedAt: missionTestTime, Deadline: missionTestTime.Add(time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("Build() error = %v, want stale context rejection", err)
	}
	_, err = Build(BuildInput{ID: "MSN-001", ProjectID: "PRJ-TEST", Context: model.ContextSlice{ID: "CTX-001", SliceHash: "context-hash", GoalVersion: 1}, GoalVersion: 1, Lease: model.Lease{ID: "LSE-001", GoalVersion: 1, Status: "released"}, TaskIDs: []string{"TSK-001"}, CompletionCriteria: []string{"done"}, AllowedScopes: []string{"src"}, Skills: []SkillGrant{{Name: "build", Version: "1"}}, Assignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"}, RiskLevel: "L2", EnvironmentID: "ENV-001", PolicyVersion: "POL-1", IssuedAt: missionTestTime, Deadline: missionTestTime.Add(time.Hour)})
	if err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("Build() error = %v, want stale lease rejection", err)
	}
}

func TestMissionRejectsBuildAndVerifyAssignedToSameLogicalAgent(t *testing.T) {
	input := validBuildInput()
	input.Assignments = map[model.AgentFunction]string{model.FunctionBuild: "AGT-ONE", model.FunctionVerify: "AGT-ONE"}
	input.Lease.SubjectID = "AGT-ONE"
	_, err := Build(input)
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("Build() error = %v, want duty separation rejection", err)
	}
}

func TestBuildMissionRejectsUnknownRiskLevel(t *testing.T) {
	input := validBuildInput()
	input.RiskLevel = "L4"
	_, err := Build(input)
	if err == nil || !strings.Contains(err.Error(), "risk") {
		t.Fatalf("Build() error = %v, want unknown risk rejection", err)
	}
}

var missionTestTime = time.Date(2026, 8, 11, 1, 0, 0, 0, time.UTC)

func validBuildInput() BuildInput {
	return BuildInput{ID: "MSN-001", ProjectID: "PRJ-TEST", Context: model.ContextSlice{ID: "CTX-001", TaskID: "TSK-001", SliceHash: "context-hash", GoalVersion: 1}, Lease: model.Lease{ID: "LSE-001", TaskID: "TSK-001", SubjectKind: "agent", SubjectID: "AGT-BUILD", ContextID: "CTX-001", GoalVersion: 1, EnvironmentID: "ENV-001", AllowedScopes: []string{"src", "cmd"}, AllowedSkills: []string{"build", "verify"}, Status: "active"}, GoalVersion: 1, TaskIDs: []string{"TSK-001"}, CompletionCriteria: []string{"done"}, AllowedScopes: []string{"src"}, Skills: []SkillGrant{{Name: "build", Version: "1"}}, Assignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"}, RiskLevel: "L2", EnvironmentID: "ENV-001", PolicyVersion: "POL-1", IssuedAt: missionTestTime, Deadline: missionTestTime.Add(time.Hour)}
}

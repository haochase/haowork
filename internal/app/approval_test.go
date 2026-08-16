package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/mission"
	"github.com/haochase/haowork/internal/model"
)

func TestApprovalRequiresHumanDecisionAndExactPayloadHash(t *testing.T) {
	service, repository := newWorkflowService(t)
	request, err := service.RequestApproval(context.Background(), "mission", "MSN-001", "payload-a", "L2", agent("AGT-REQUESTER"))
	if err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)
	_, err = service.DecideApproval(context.Background(), request.ID, "payload-b", "approved", "looks good", model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("DecideApproval() error = %v, want ErrConflict", err)
	}
	if got := len(repository.events); got != before+1 {
		t.Fatalf("event count = %d, want invalidation only", got)
	}
	_, err = service.DecideApproval(context.Background(), request.ID, "payload-a", "approved", "looks good", agent("AGT-DECIDER"))
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("DecideApproval() error = %v, want ErrApprovalRequired", err)
	}
}

func TestApprovalCannotBeDecidedByRequestingAgent(t *testing.T) {
	service, repository := newWorkflowService(t)
	request, err := service.RequestApproval(context.Background(), "mission", "MSN-001", "payload-a", "L2", owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)
	_, err = service.DecideApproval(context.Background(), request.ID, "payload-a", "approved", "looks good", owner("USR-OWNER"))
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("DecideApproval() error = %v, want ErrApprovalRequired", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRequiresApprovedCanonicalRequest(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L2"
	input.Actor = agent("AGT-REQUESTER")
	before := len(repository.events)

	_, err := service.IssueMission(context.Background(), input)
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("IssueMission() error = %v, want ErrApprovalRequired without an approved request", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}

	state, err := model.Reduce(repository.events)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := mission.Build(mission.BuildInput{
		ID: input.MissionID, ProjectID: "PRJ-TEST", Context: state.Contexts[input.ContextID], Lease: state.Leases["LSE-001"], GoalVersion: state.Goal.Version,
		TaskIDs: input.TaskIDs, CompletionCriteria: input.CompletionCriteria, AllowedScopes: input.AllowedScopes, Skills: input.Skills,
		Assignments: input.Assignments, RiskLevel: input.RiskLevel, EnvironmentID: input.EnvironmentID, PolicyVersion: input.PolicyVersion,
		IssuedAt: testTime, Deadline: input.Deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events,
		model.Event{ID: "EVT-APR-REQ", Type: "approval.requested", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-001", Actor: input.Actor, OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalRequested{Approval: model.ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: envelope.ID, PayloadSHA256: envelope.Hash, RiskLevel: "L2", RequesterID: input.Actor.ID, RequestedAt: testTime.UTC()}})},
		model.Event{ID: "EVT-APR-DEC", Type: "approval.decided", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-001", Actor: model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}, OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalDecided{ApprovalID: "APR-001", PayloadSHA256: envelope.Hash, Decision: "approved", DeciderID: "USR-LEAD", DecidedAt: testTime.UTC()})},
	)

	issued, err := service.IssueMission(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ID != envelope.ID || issued.Hash != envelope.Hash {
		t.Fatalf("issued mission = %#v, want approved canonical envelope %#v", issued, envelope)
	}
}

func TestPrepareMissionStabilizesHighRiskApprovalAcrossClockAdvance(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	clock := &advancingClock{values: []time.Time{
		testTime, testTime.Add(time.Minute), testTime.Add(2 * time.Minute), testTime.Add(3 * time.Minute),
	}}
	service.clock = clock
	input.RiskLevel = "L2"
	input.Actor = agent("AGT-REQUESTER")
	input.MissionID = "MSN-PREPARED"
	input.IssuedAt = time.Time{}
	before := len(repository.events)

	prepared, err := service.PrepareMission(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.RequestApproval(context.Background(), "mission", prepared.ID, prepared.Hash, "L2", input.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DecideApproval(context.Background(), approval.ID, prepared.Hash, "approved", "approved", model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}); err != nil {
		t.Fatal(err)
	}
	input.IssuedAt = prepared.IssuedAt
	issued, err := service.IssueMission(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ID != prepared.ID || issued.IssuedAt != prepared.IssuedAt || issued.Hash != prepared.Hash {
		t.Fatalf("issued mission = %#v, want prepared mission %#v", issued, prepared)
	}
	if got := len(repository.events); got != before+3 {
		t.Fatalf("event count = %d, want %d", got, before+3)
	}
}

func TestIssueMissionRequiresStableFieldsForHighRisk(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L2"
	input.Actor = agent("AGT-REQUESTER")
	state, err := model.Reduce(repository.events)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := mission.Build(mission.BuildInput{
		ID: input.MissionID, ProjectID: "PRJ-TEST", Context: state.Contexts[input.ContextID], Lease: state.Leases["LSE-001"], GoalVersion: state.Goal.Version,
		TaskIDs: input.TaskIDs, CompletionCriteria: input.CompletionCriteria, AllowedScopes: input.AllowedScopes, Skills: input.Skills,
		Assignments: input.Assignments, RiskLevel: input.RiskLevel, EnvironmentID: input.EnvironmentID, PolicyVersion: input.PolicyVersion,
		IssuedAt: testTime, Deadline: input.Deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	appendMissionApproval(t, repository, model.MissionEnvelope(envelope), input.Actor, model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead})
	input.IssuedAt = time.Time{}
	before := len(repository.events)

	_, err = service.IssueMission(context.Background(), input)
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("IssueMission() error = %v, want ErrApprovalRequired without explicit IssuedAt", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRequiresHumanOwnerForL3(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L3"
	input.Actor = agent("AGT-ISSUER")
	state, err := model.Reduce(repository.events)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := mission.Build(mission.BuildInput{
		ID: input.MissionID, ProjectID: "PRJ-TEST", Context: state.Contexts[input.ContextID], Lease: state.Leases["LSE-001"], GoalVersion: state.Goal.Version,
		TaskIDs: input.TaskIDs, CompletionCriteria: input.CompletionCriteria, AllowedScopes: input.AllowedScopes, Skills: input.Skills,
		Assignments: input.Assignments, RiskLevel: input.RiskLevel, EnvironmentID: input.EnvironmentID, PolicyVersion: input.PolicyVersion,
		IssuedAt: testTime, Deadline: input.Deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events,
		model.Event{ID: "EVT-APR-REQ-L3", Type: "approval.requested", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-L3", Actor: agent("AGT-REQUESTER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalRequested{Approval: model.ApprovalRequest{ID: "APR-L3", SubjectType: "mission", SubjectID: envelope.ID, PayloadSHA256: envelope.Hash, RiskLevel: "L3", RequesterID: "AGT-REQUESTER", RequestedAt: testTime.UTC()}})},
		model.Event{ID: "EVT-APR-DEC-L3", Type: "approval.decided", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-L3", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalDecided{ApprovalID: "APR-L3", PayloadSHA256: envelope.Hash, Decision: "approved", DeciderID: "USR-OWNER", DecidedAt: testTime.UTC()})},
	)

	_, err = service.IssueMission(context.Background(), input)
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("IssueMission() error = %v, want ErrApprovalRequired for non-owner L3 issuer", err)
	}
}

func TestMissionAPIsRejectUnknownRiskLevel(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L4"
	input.Actor = agent("AGT-REQUESTER")
	before := len(repository.events)

	_, err := service.IssueMission(context.Background(), input)
	if err == nil {
		t.Fatal("IssueMission() succeeded, want unknown risk rejection")
	}
	if _, err := service.RequestApproval(context.Background(), "mission", input.MissionID, "payload-hash", "L4", input.Actor); err == nil {
		t.Fatal("RequestApproval() succeeded, want unknown risk rejection")
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRejectsLeaseSubjectOutsideBuildAssignment(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L1"
	input.Actor = owner("USR-OWNER")
	repository.events[len(repository.events)-1].Payload = mustJSON(t, model.LeaseIssued{Lease: model.Lease{
		ID: "LSE-001", TaskID: input.TaskIDs[0], SubjectKind: "agent", SubjectID: "AGT-OTHER",
		EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, Status: "active",
	}})
	before := len(repository.events)

	_, err := service.IssueMission(context.Background(), input)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("IssueMission() error = %v, want ErrConflict for lease subject outside build assignment", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRejectsAmbiguousMatchingLeases(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L1"
	input.Actor = owner("USR-OWNER")
	repository.events = append(repository.events, model.Event{
		ID: "EVT-LSE-002", Type: "lease.issued", ProjectID: "PRJ-TEST", GoalVersion: 1,
		AggregateType: "lease", AggregateID: "LSE-002", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(),
		Payload: mustJSON(t, model.LeaseIssued{Lease: model.Lease{ID: "LSE-002", TaskID: input.TaskIDs[0], SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, Status: "active"}}),
	})
	before := len(repository.events)

	_, err := service.IssueMission(context.Background(), input)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("IssueMission() error = %v, want ErrConflict for ambiguous leases", err)
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRequiresSingleTaskBinding(t *testing.T) {
	service, repository, input := missionApprovalFixture(t)
	input.RiskLevel = "L1"
	input.Actor = owner("USR-OWNER")
	input.TaskIDs = []string{input.TaskIDs[0], input.TaskIDs[0]}
	before := len(repository.events)

	_, err := service.IssueMission(context.Background(), input)
	if err == nil {
		t.Fatal("IssueMission() succeeded, want single task binding rejection")
	}
	if got := len(repository.events); got != before {
		t.Fatalf("event count = %d, want %d", got, before)
	}
}

func TestIssueMissionRejectsCapabilitiesOutsideLease(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*IssueMissionInput)
	}{
		{name: "scope", mutate: func(input *IssueMissionInput) { input.AllowedScopes = []string{"internal/app", "internal/model"} }},
		{name: "skill", mutate: func(input *IssueMissionInput) {
			input.Skills = []mission.SkillGrant{{Name: "build", Version: "1"}, {Name: "verify", Version: "1"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, repository, input := missionApprovalFixture(t)
			input.RiskLevel = "L1"
			input.Actor = owner("USR-OWNER")
			test.mutate(&input)
			before := len(repository.events)

			if _, err := service.IssueMission(context.Background(), input); err == nil {
				t.Fatal("IssueMission() succeeded, want lease capability rejection")
			}
			if got := len(repository.events); got != before {
				t.Fatalf("event count = %d, want %d", got, before)
			}
		})
	}
}

func appendMissionApproval(t *testing.T, repository *memoryRepository, envelope model.MissionEnvelope, requester, decider model.Actor) {
	t.Helper()
	repository.events = append(repository.events,
		model.Event{ID: "EVT-APR-REQ-PREPARED", Type: "approval.requested", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-PREPARED", Actor: requester, OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalRequested{Approval: model.ApprovalRequest{ID: "APR-PREPARED", SubjectType: "mission", SubjectID: envelope.ID, PayloadSHA256: envelope.Hash, RiskLevel: envelope.RiskLevel, RequesterID: requester.ID, RequestedAt: testTime.UTC()}})},
		model.Event{ID: "EVT-APR-DEC-PREPARED", Type: "approval.decided", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-PREPARED", Actor: decider, OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ApprovalDecided{ApprovalID: "APR-PREPARED", PayloadSHA256: envelope.Hash, Decision: "approved", DeciderID: decider.ID, DecidedAt: testTime.UTC()})},
	)
}

func missionApprovalFixture(t *testing.T) (*Service, *memoryRepository, IssueMissionInput) {
	t.Helper()
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Govern changes", Tasks: []TaskInput{{Title: "Implement gate", AcceptanceCriteria: []string{"tests pass"}}}, Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	repository.events = append(repository.events,
		model.Event{ID: "EVT-AGT-BUILD", Type: "agent.identity.registered", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "agent", AggregateID: "AGT-BUILD", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: "AGT-BUILD", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionBuild}})},
		model.Event{ID: "EVT-AGT-VERIFY", Type: "agent.identity.registered", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "agent", AggregateID: "AGT-VERIFY", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.AgentIdentityRegistered{Agent: model.LogicalAgent{ID: "AGT-VERIFY", SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: model.FunctionVerify}})},
		model.Event{ID: "EVT-CTX-001", Type: "context.issued", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "context", AggregateID: "CTX-001", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.ContextIssued{Context: model.ContextSlice{ID: "CTX-001", TaskID: tasks[0].ID, GoalVersion: 1, Revision: 1, Summary: "task context", SliceHash: "context-hash"}})},
		model.Event{ID: "EVT-LSE-001", Type: "lease.issued", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "lease", AggregateID: "LSE-001", Actor: owner("USR-OWNER"), OccurredAt: testTime.UTC(), Payload: mustJSON(t, model.LeaseIssued{Lease: model.Lease{ID: "LSE-001", TaskID: tasks[0].ID, SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/app"}, AllowedSkills: []string{"build"}, Status: "active"}})},
	)
	return service, repository, IssueMissionInput{
		MissionID: "MSN-APPROVED", TaskIDs: []string{tasks[0].ID}, CompletionCriteria: []string{"tests pass"}, AllowedScopes: []string{"internal/app"}, Skills: []mission.SkillGrant{{Name: "build", Version: "1"}},
		ContextID: "CTX-001", EnvironmentID: "ENV-001", PolicyVersion: "POL-1", Assignments: map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"},
		IssuedAt: testTime, Deadline: testTime.Add(time.Hour),
	}
}

type advancingClock struct {
	values []time.Time
	index  int
}

func (clock *advancingClock) Now() time.Time {
	value := clock.values[clock.index]
	if clock.index < len(clock.values)-1 {
		clock.index++
	}
	return value
}

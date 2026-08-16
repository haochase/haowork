package model

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReduceRejectsAgentIdentityWithHumanGovernanceRole(t *testing.T) {
	events := agentTestEvents(t,
		Event{ID: "EVT-AGENT", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-001", SubjectKind: ActorAgent, GovernanceRole: RoleOwner, Function: FunctionBuild}})},
	)

	_, err := Reduce(events)
	if err == nil || !strings.Contains(err.Error(), "governance role") {
		t.Fatalf("Reduce() error = %v, want agent governance role rejection", err)
	}
}

func TestReduceRuntimeRebindPreservesLogicalIdentityAndAdvancesRevision(t *testing.T) {
	events := agentTestEvents(t,
		Event{ID: "EVT-AGENT", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-001", SubjectKind: ActorAgent, GovernanceRole: RoleAgent, Function: FunctionBuild}})},
		Event{ID: "EVT-BIND-1", Type: "agent.runtime.bound", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RuntimeBound{Binding: RuntimeBinding{LogicalActorID: "AGT-001", Revision: 1, EnvironmentID: "ENV-A", AgentTeamsInstanceID: "TEAM-A", RuntimePrincipalID: "principal-a", LeaderRoomID: "leader-a", TeamRoomID: "team-a"}})},
		Event{ID: "EVT-BIND-2", Type: "agent.runtime.bound", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RuntimeBound{Binding: RuntimeBinding{LogicalActorID: "AGT-001", Revision: 2, EnvironmentID: "ENV-B", AgentTeamsInstanceID: "TEAM-B", RuntimePrincipalID: "principal-b", LeaderRoomID: "leader-b", TeamRoomID: "team-b"}})},
	)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Agents["AGT-001"].ID; got != "AGT-001" {
		t.Fatalf("logical identity = %q, want AGT-001", got)
	}
	if len(state.RuntimeBindings["AGT-001"]) != 2 {
		t.Fatalf("binding history length = %d, want 2", len(state.RuntimeBindings["AGT-001"]))
	}
	old, current := state.RuntimeBindings["AGT-001"][0], state.RuntimeBindings["AGT-001"][1]
	if old.Status != "inactive" || current.Status != "active" || current.Revision != 2 {
		t.Fatalf("bindings = %#v, %#v; want inactive revision 1 then active revision 2", old, current)
	}
	if current.RuntimePrincipalID != "principal-b" || current.EnvironmentID != "ENV-B" || current.LeaderRoomID != "leader-b" || current.TeamRoomID != "team-b" {
		t.Fatalf("current binding = %#v, want replacement runtime values", current)
	}
}

func TestReduceRuntimeBindingRequiresHumanOwner(t *testing.T) {
	registered := Event{ID: "EVT-AGENT", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-001", SubjectKind: ActorAgent, GovernanceRole: RoleAgent, Function: FunctionBuild}})}
	for _, actor := range []Actor{
		{ID: "AGT-001", Kind: ActorAgent, Role: RoleAgent},
		{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead},
	} {
		events := agentTestEvents(t, registered, Event{ID: "EVT-BIND", Type: "agent.runtime.bound", AggregateType: "agent", AggregateID: "AGT-001", Actor: actor, Payload: mustAgentPayload(t, RuntimeBound{Binding: RuntimeBinding{LogicalActorID: "AGT-001", Revision: 1, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "TEAM-001", RuntimePrincipalID: "runtime-001"}})})
		if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), "human owner") {
			t.Fatalf("Reduce() actor %#v error = %v, want owner rejection", actor, err)
		}
	}
}

func TestReduceRejectsForgedAgentEventAggregates(t *testing.T) {
	registered := Event{ID: "EVT-AGENT", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-001", SubjectKind: ActorAgent, GovernanceRole: RoleAgent, Function: FunctionBuild}})}
	bound := Event{ID: "EVT-BOUND", Type: "agent.runtime.bound", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RuntimeBound{Binding: RuntimeBinding{LogicalActorID: "AGT-001", Revision: 1, EnvironmentID: "ENV-001", AgentTeamsInstanceID: "TEAM-001", RuntimePrincipalID: "runtime-001"}})}
	unbound := Event{ID: "EVT-UNBOUND", Type: "agent.runtime.unbound", AggregateType: "agent", AggregateID: "AGT-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RuntimeUnbound{LogicalActorID: "AGT-001", Revision: 1})}

	for _, test := range []struct {
		name   string
		events []Event
		mutate func(*Event)
	}{
		{name: "registration type", events: []Event{registered}, mutate: func(event *Event) { event.AggregateType = "mission" }},
		{name: "registration id", events: []Event{registered}, mutate: func(event *Event) { event.AggregateID = "AGT-OTHER" }},
		{name: "binding type", events: []Event{registered, bound}, mutate: func(event *Event) { event.AggregateType = "mission" }},
		{name: "binding id", events: []Event{registered, bound}, mutate: func(event *Event) { event.AggregateID = "AGT-OTHER" }},
		{name: "unbinding type", events: []Event{registered, bound, unbound}, mutate: func(event *Event) { event.AggregateType = "mission" }},
		{name: "unbinding id", events: []Event{registered, bound, unbound}, mutate: func(event *Event) { event.AggregateID = "AGT-OTHER" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := append([]Event(nil), test.events...)
			test.mutate(&events[len(events)-1])
			events = agentTestEvents(t, events...)

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want forged agent aggregate rejection")
			}
		})
	}
}

func TestReduceRejectsMissionReplayWithForgedOrStaleBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MissionEnvelope, *[]Event)
		want   string
	}{
		{name: "forged hash", mutate: func(mission *MissionEnvelope, _ *[]Event) { mission.Hash = "forged" }, want: "hash"},
		{name: "stale context hash", mutate: func(mission *MissionEnvelope, _ *[]Event) {
			mission.ContextHash = "stale-context-hash"
			mission.Hash = missionHashForTest(t, *mission)
		}, want: "context"},
		{name: "revoked lease", mutate: func(_ *MissionEnvelope, events *[]Event) {
			*events = append(*events, missionReplayEvent(t, "EVT-LSE-REVOKED", "lease.revoked", LeaseRevoked{LeaseID: "LSE-001"}))
		}, want: "lease"},
		{name: "unknown task", mutate: func(mission *MissionEnvelope, _ *[]Event) {
			mission.GovernanceTaskIDs = []string{"TSK-UNKNOWN"}
			mission.Hash = missionHashForTest(t, *mission)
		}, want: "task"},
		{name: "wrong role assignment", mutate: func(mission *MissionEnvelope, _ *[]Event) {
			mission.RoleAssignments[FunctionBuild] = "AGT-VERIFY"
			mission.Hash = missionHashForTest(t, *mission)
		}, want: "assignment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, envelope := validMissionReplayEvents(t)
			test.mutate(&envelope, &events)
			events = append(events, missionReplayEvent(t, "EVT-MSN-001", "mission.issued", MissionIssued{Envelope: envelope}))

			_, err := Reduce(events)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Reduce() error = %v, want %q replay rejection", err, test.want)
			}
		})
	}
}

func TestReduceRejectsMissionReplayWithLeaseSubjectOutsideBuildAssignment(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	events[len(events)-1].Payload = mustAgentPayload(t, LeaseIssued{Lease: Lease{
		ID: "LSE-001", TaskID: "TSK-001", SubjectKind: "agent", SubjectID: "AGT-OTHER",
		EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, Status: "active",
	}})
	events = append(events, missionReplayEvent(t, "EVT-MSN-LEASE-SUBJECT", "mission.issued", MissionIssued{Envelope: mission}))

	if _, err := Reduce(events); err == nil || !strings.Contains(strings.ToLower(err.Error()), "lease") {
		t.Fatalf("Reduce() error = %v, want lease subject rejection", err)
	}
}

func TestReduceRejectsMissionReplayWithAmbiguousMatchingLeases(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	events = append(events, Event{ID: "EVT-LSE-002", Type: "lease.issued", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "lease", AggregateID: "LSE-002", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, LeaseIssued{Lease: Lease{ID: "LSE-002", TaskID: "TSK-001", SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, Status: "active"}})})
	events = append(events, missionReplayEvent(t, "EVT-MSN-AMBIGUOUS", "mission.issued", MissionIssued{Envelope: mission}))

	if _, err := Reduce(events); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("Reduce() error = %v, want ambiguous lease rejection", err)
	}
}

func TestReduceRejectsMissionReplayWithMultipleTaskBindings(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	events[len(events)-1].Payload = mustAgentPayload(t, LeaseIssued{Lease: Lease{ID: "LSE-001", TaskID: "TSK-002", SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, Status: "active"}})
	events = append(events,
		Event{ID: "EVT-REQ-002", Type: "requirement.planned", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "requirement", AggregateID: "REQ-002", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RequirementPlanned{Requirement: Requirement{ID: "REQ-002", GoalVersion: 1, Title: "second task"}, Tasks: []Task{{ID: "TSK-002", RequirementID: "REQ-002", GoalVersion: 1, Title: "second", AcceptanceCriteria: []string{"done"}}}})},
		Event{ID: "EVT-REQ-002-APPROVED", Type: "requirement.approved", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "requirement", AggregateID: "REQ-002", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RequirementApproved{RequirementID: "REQ-002"})},
	)
	mission.GovernanceTaskIDs = []string{"TSK-001", "TSK-002"}
	mission.Hash = missionHashForTest(t, mission)
	events = append(events, missionReplayEvent(t, "EVT-MSN-MULTI-TASK", "mission.issued", MissionIssued{Envelope: mission}))

	if _, err := Reduce(events); err == nil || !strings.Contains(strings.ToLower(err.Error()), "task") {
		t.Fatalf("Reduce() error = %v, want single task binding rejection", err)
	}
}

func TestReduceRejectsMissionReplayWithoutValidApproval(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*MissionEnvelope, *[]Event) Actor
	}{
		{name: "L2 without approval", prepare: func(mission *MissionEnvelope, _ *[]Event) Actor {
			mission.RiskLevel = "L2"
			mission.Hash = missionHashForTest(t, *mission)
			return Actor{ID: "AGT-ISSUER", Kind: ActorAgent, Role: RoleAgent}
		}},
		{name: "L3 without approval", prepare: func(mission *MissionEnvelope, _ *[]Event) Actor {
			mission.RiskLevel = "L3"
			mission.Hash = missionHashForTest(t, *mission)
			return Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}
		}},
		{name: "L2 owner approval", prepare: func(mission *MissionEnvelope, events *[]Event) Actor {
			mission.RiskLevel = "L2"
			mission.Hash = missionHashForTest(t, *mission)
			appendMissionApprovalEvents(t, events, *mission, Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner})
			return Actor{ID: "AGT-ISSUER", Kind: ActorAgent, Role: RoleAgent}
		}},
		{name: "L3 agent issuer", prepare: func(mission *MissionEnvelope, events *[]Event) Actor {
			mission.RiskLevel = "L3"
			mission.Hash = missionHashForTest(t, *mission)
			appendMissionApprovalEvents(t, events, *mission, Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner})
			return Actor{ID: "AGT-ISSUER", Kind: ActorAgent, Role: RoleAgent}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, mission := validMissionReplayEvents(t)
			issuer := test.prepare(&mission, &events)
			issued := missionReplayEvent(t, "EVT-MSN-RISK", "mission.issued", MissionIssued{Envelope: mission})
			issued.Actor = issuer
			events = append(events, issued)

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want mission approval replay rejection")
			}
		})
	}
}

func TestReduceRejectsForgedApprovalRequestedReplay(t *testing.T) {
	requester := Actor{ID: "USR-ATTACKER", Kind: ActorHuman, Role: RoleLead}
	events := agentTestEvents(t,
		Event{ID: "EVT-APR-REQ", Type: "approval.requested", AggregateType: "approval", AggregateID: "APR-001", Actor: requester, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: "MSN-001", PayloadSHA256: "payload-hash", RiskLevel: "L2", RequesterID: "USR-VICTIM"}})},
		Event{ID: "EVT-APR-DEC", Type: "approval.decided", AggregateType: "approval", AggregateID: "APR-001", Actor: requester, Payload: mustAgentPayload(t, ApprovalDecided{ApprovalID: "APR-001", PayloadSHA256: "payload-hash", Decision: "approved", DeciderID: requester.ID})},
	)

	if _, err := Reduce(events); err == nil {
		t.Fatal("Reduce() succeeded, want forged requester self-approval rejection")
	}
}

func TestReduceRejectsUnauthorizedApprovalInvalidationReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		risk      string
		requester Actor
		actor     Actor
	}{
		{name: "agent invalidates L2", risk: "L2", requester: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, actor: Actor{ID: "AGT-INVALIDATOR", Kind: ActorAgent, Role: RoleAgent}},
		{name: "owner invalidates L2", risk: "L2", requester: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}},
		{name: "lead invalidates L3", risk: "L3", requester: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}},
		{name: "requester invalidates L2", risk: "L2", requester: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}, actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := agentTestEvents(t,
				Event{ID: "EVT-APR-REQ", Type: "approval.requested", AggregateType: "approval", AggregateID: "APR-001", Actor: test.requester, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: "MSN-001", PayloadSHA256: "payload-hash", RiskLevel: test.risk, RequesterID: test.requester.ID}})},
				Event{ID: "EVT-APR-INV", Type: "approval.invalidated", AggregateType: "approval", AggregateID: "APR-001", Actor: test.actor, Payload: mustAgentPayload(t, ApprovalInvalidated{ApprovalID: "APR-001", Reason: "changed"})},
			)

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want unauthorized approval invalidation rejection")
			}
		})
	}
}

func TestVerifyMissionEnvelopeRejectsBlankCanonicalContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MissionEnvelope)
	}{
		{name: "task", mutate: func(mission *MissionEnvelope) { mission.GovernanceTaskIDs = []string{""} }},
		{name: "criterion", mutate: func(mission *MissionEnvelope) { mission.CompletionCriteria = []string{""} }},
		{name: "scope", mutate: func(mission *MissionEnvelope) { mission.AllowedScopes = []string{""} }},
		{name: "skill", mutate: func(mission *MissionEnvelope) { mission.AllowedSkills = []MissionSkillGrant{{Name: "", Version: "1"}} }},
		{name: "assignment", mutate: func(mission *MissionEnvelope) { mission.RoleAssignments[FunctionManager] = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, mission := validMissionReplayEvents(t)
			test.mutate(&mission)
			mission.Hash = missionHashForTest(t, mission)

			if err := VerifyMissionEnvelope(mission); err == nil {
				t.Fatal("VerifyMissionEnvelope() succeeded, want blank content rejection")
			}
		})
	}
}

func TestReduceRejectsMissionReplayWithBlankCanonicalContent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MissionEnvelope)
	}{
		{name: "criterion", mutate: func(mission *MissionEnvelope) { mission.CompletionCriteria = []string{""} }},
		{name: "scope", mutate: func(mission *MissionEnvelope) { mission.AllowedScopes = []string{""} }},
		{name: "skill", mutate: func(mission *MissionEnvelope) { mission.AllowedSkills = []MissionSkillGrant{{Name: "", Version: "1"}} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, mission := validMissionReplayEvents(t)
			test.mutate(&mission)
			mission.Hash = missionHashForTest(t, mission)
			events = append(events, missionReplayEvent(t, "EVT-MSN-BLANK", "mission.issued", MissionIssued{Envelope: mission}))

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want blank canonical content rejection")
			}
		})
	}
}

func TestReduceRejectsMissionReplayWithCapabilitiesOutsideLease(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MissionEnvelope)
	}{
		{name: "scope", mutate: func(mission *MissionEnvelope) { mission.AllowedScopes = []string{"internal/model", "internal/app"} }},
		{name: "skill", mutate: func(mission *MissionEnvelope) {
			mission.AllowedSkills = []MissionSkillGrant{{Name: "build", Version: "1"}, {Name: "verify", Version: "1"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, mission := validMissionReplayEvents(t)
			test.mutate(&mission)
			mission.Hash = missionHashForTest(t, mission)
			events = append(events, missionReplayEvent(t, "EVT-MSN-CAPABILITY", "mission.issued", MissionIssued{Envelope: mission}))

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want lease capability rejection")
			}
		})
	}
}

func TestVerifyMissionEnvelopeRejectsCapabilitiesOutsideLease(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	mission.AllowedScopes = []string{"internal/model", "internal/app"}
	mission.Hash = missionHashForTest(t, mission)

	if err := VerifyMissionEnvelope(mission, state.Leases[mission.LeaseID]); err == nil {
		t.Fatal("VerifyMissionEnvelope() succeeded, want lease capability rejection")
	}
}

func TestReduceRejectsUnauthorizedMissionInvalidationReplay(t *testing.T) {
	for _, test := range []struct {
		name  string
		risk  string
		actor Actor
	}{
		{name: "agent invalidates L1", risk: "L1", actor: Actor{ID: "AGT-INVALIDATOR", Kind: ActorAgent, Role: RoleAgent}},
		{name: "owner invalidates L2", risk: "L2", actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}},
		{name: "lead invalidates L3", risk: "L3", actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}},
		{name: "assigned build actor invalidates L2", risk: "L2", actor: Actor{ID: "AGT-BUILD", Kind: ActorHuman, Role: RoleLead}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, mission := validMissionReplayEvents(t)
			mission.RiskLevel = test.risk
			mission.Hash = missionHashForTest(t, mission)
			if missionApprovalRequired(mission.RiskLevel) {
				decider := Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}
				if mission.RiskLevel == "L3" {
					decider = Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}
				}
				appendMissionApprovalEvents(t, &events, mission, Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, decider)
			}
			issued := missionReplayEvent(t, "EVT-MSN-ISSUED", "mission.issued", MissionIssued{Envelope: mission})
			if mission.RiskLevel == "L3" {
				issued.Actor = Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}
			}
			events = append(events, issued)
			events = append(events, Event{ID: "EVT-MSN-INVALIDATED", Type: "mission.invalidated", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "mission", AggregateID: mission.ID, Actor: test.actor, Payload: mustAgentPayload(t, MissionInvalidated{MissionID: mission.ID, Reason: "changed"})})

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want unauthorized mission invalidation rejection")
			}
		})
	}
}

func TestReduceRejectsApprovalRequestAggregateMismatch(t *testing.T) {
	events := agentTestEvents(t,
		Event{ID: "EVT-APR-REQ", Type: "approval.requested", AggregateType: "approval", AggregateID: "APR-OTHER", Actor: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: "MSN-001", PayloadSHA256: "payload-hash", RiskLevel: "L2", RequesterID: "AGT-REQUESTER"}})},
	)

	if _, err := Reduce(events); err == nil {
		t.Fatal("Reduce() succeeded, want approval aggregate mismatch rejection")
	}
}

func TestReduceRejectsApprovalRequestAggregateTypeMismatch(t *testing.T) {
	events := agentTestEvents(t,
		Event{ID: "EVT-APR-REQ", Type: "approval.requested", AggregateType: "mission", AggregateID: "APR-001", Actor: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: "MSN-001", PayloadSHA256: "payload-hash", RiskLevel: "L2", RequesterID: "AGT-REQUESTER"}})},
	)

	if _, err := Reduce(events); err == nil {
		t.Fatal("Reduce() succeeded, want approval aggregate type mismatch rejection")
	}
}

func TestReduceRejectsUnknownMissionRiskLevel(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	mission.RiskLevel = "L4"
	mission.Hash = missionHashForTest(t, mission)
	events = append(events, missionReplayEvent(t, "EVT-MSN-L4", "mission.issued", MissionIssued{Envelope: mission}))

	if _, err := Reduce(events); err == nil {
		t.Fatal("Reduce() succeeded, want unknown mission risk rejection")
	}
}

func TestReduceRejectsMissionIssuedAndInvalidatedAggregateMismatch(t *testing.T) {
	for _, test := range []struct {
		name          string
		eventType     string
		aggregateType string
		aggregateID   string
	}{
		{name: "issued aggregate type", eventType: "mission.issued", aggregateType: "approval", aggregateID: "MSN-001"},
		{name: "issued aggregate id", eventType: "mission.issued", aggregateType: "mission", aggregateID: "MSN-OTHER"},
		{name: "invalidated aggregate type", eventType: "mission.invalidated", aggregateType: "approval", aggregateID: "MSN-001"},
		{name: "invalidated aggregate id", eventType: "mission.invalidated", aggregateType: "mission", aggregateID: "MSN-OTHER"},
	} {
		t.Run(test.name, func(t *testing.T) {
			events, mission := validMissionReplayEvents(t)
			if test.eventType == "mission.invalidated" {
				events = append(events, missionReplayEvent(t, "EVT-MSN-ISSUED", "mission.issued", MissionIssued{Envelope: mission}))
				events = append(events, Event{ID: "EVT-MSN-INVALIDATED", Type: test.eventType, ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: test.aggregateType, AggregateID: test.aggregateID, Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, MissionInvalidated{MissionID: mission.ID})})
			} else {
				event := missionReplayEvent(t, "EVT-MSN-ISSUED", test.eventType, MissionIssued{Envelope: mission})
				event.AggregateType = test.aggregateType
				event.AggregateID = test.aggregateID
				events = append(events, event)
			}

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want mission aggregate mismatch rejection")
			}
		})
	}
}

func TestReduceRejectsApprovalDecisionAndInvalidationAggregateMismatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		event Event
	}{
		{name: "decision aggregate type", event: Event{ID: "EVT-APR-DEC", Type: "approval.decided", AggregateType: "mission", AggregateID: "APR-001", Actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}, Payload: mustAgentPayload(t, ApprovalDecided{ApprovalID: "APR-001", PayloadSHA256: "payload-hash", Decision: "approved", DeciderID: "USR-LEAD"})}},
		{name: "decision aggregate id", event: Event{ID: "EVT-APR-DEC", Type: "approval.decided", AggregateType: "approval", AggregateID: "APR-OTHER", Actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}, Payload: mustAgentPayload(t, ApprovalDecided{ApprovalID: "APR-001", PayloadSHA256: "payload-hash", Decision: "approved", DeciderID: "USR-LEAD"})}},
		{name: "invalidation aggregate type", event: Event{ID: "EVT-APR-INV", Type: "approval.invalidated", AggregateType: "mission", AggregateID: "APR-001", Actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}, Payload: mustAgentPayload(t, ApprovalInvalidated{ApprovalID: "APR-001", Reason: "changed"})}},
		{name: "invalidation aggregate id", event: Event{ID: "EVT-APR-INV", Type: "approval.invalidated", AggregateType: "approval", AggregateID: "APR-OTHER", Actor: Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}, Payload: mustAgentPayload(t, ApprovalInvalidated{ApprovalID: "APR-001", Reason: "changed"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := agentTestEvents(t,
				Event{ID: "EVT-APR-REQ", Type: "approval.requested", AggregateType: "approval", AggregateID: "APR-001", Actor: Actor{ID: "AGT-REQUESTER", Kind: ActorAgent, Role: RoleAgent}, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: "MSN-001", PayloadSHA256: "payload-hash", RiskLevel: "L2", RequesterID: "AGT-REQUESTER"}})},
			)
			test.event.ProjectID = "PRJ-TEST"
			test.event.GoalVersion = 1
			events = append(events, test.event)

			if _, err := Reduce(events); err == nil {
				t.Fatal("Reduce() succeeded, want approval aggregate mismatch rejection")
			}
		})
	}
}

func validMissionReplayEvents(t *testing.T) ([]Event, MissionEnvelope) {
	t.Helper()
	events := agentTestEvents(t,
		Event{ID: "EVT-REQ", Type: "requirement.planned", AggregateType: "requirement", AggregateID: "REQ-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RequirementPlanned{Requirement: Requirement{ID: "REQ-001", GoalVersion: 1, Title: "governed mission"}, Tasks: []Task{{ID: "TSK-001", RequirementID: "REQ-001", GoalVersion: 1, Title: "build", AcceptanceCriteria: []string{"done"}}}})},
		Event{ID: "EVT-REQ-APPROVED", Type: "requirement.approved", AggregateType: "requirement", AggregateID: "REQ-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, RequirementApproved{RequirementID: "REQ-001"})},
		Event{ID: "EVT-AGT-BUILD", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-BUILD", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-BUILD", SubjectKind: ActorAgent, GovernanceRole: RoleAgent, Function: FunctionBuild}})},
		Event{ID: "EVT-AGT-VERIFY", Type: "agent.identity.registered", AggregateType: "agent", AggregateID: "AGT-VERIFY", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, AgentIdentityRegistered{Agent: LogicalAgent{ID: "AGT-VERIFY", SubjectKind: ActorAgent, GovernanceRole: RoleAgent, Function: FunctionVerify}})},
		Event{ID: "EVT-CTX", Type: "context.issued", AggregateType: "context", AggregateID: "CTX-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, ContextIssued{Context: ContextSlice{ID: "CTX-001", TaskID: "TSK-001", GoalVersion: 1, Revision: 1, Summary: "approved context", SliceHash: "context-hash"}})},
		Event{ID: "EVT-LSE", Type: "lease.issued", AggregateType: "lease", AggregateID: "LSE-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, LeaseIssued{Lease: Lease{ID: "LSE-001", TaskID: "TSK-001", SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "ENV-001", ContextID: "CTX-001", GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/model"}, AllowedSkills: []string{"build"}, Status: "active"}})},
	)
	envelope := MissionEnvelope{ID: "MSN-001", ProjectID: "PRJ-TEST", ContextID: "CTX-001", ContextHash: "context-hash", LeaseID: "LSE-001", PolicyVersion: "POL-1", GoalVersion: 1, GovernanceTaskIDs: []string{"TSK-001"}, CompletionCriteria: []string{"done"}, AllowedScopes: []string{"internal/model"}, AllowedSkills: []MissionSkillGrant{{Name: "build", Version: "1"}}, RoleAssignments: map[AgentFunction]string{FunctionBuild: "AGT-BUILD", FunctionVerify: "AGT-VERIFY"}, RiskLevel: "L1", EnvironmentID: "ENV-001", IssuedAt: time.Date(2026, 8, 11, 1, 0, 0, 0, time.UTC), Deadline: time.Date(2026, 8, 11, 2, 0, 0, 0, time.UTC)}
	envelope.Hash = missionHashForTest(t, envelope)
	return events, envelope
}

func missionReplayEvent(t *testing.T, id, eventType string, payload any) Event {
	t.Helper()
	return Event{ID: id, Type: eventType, ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "mission", AggregateID: "MSN-001", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, Payload: mustAgentPayload(t, payload)}
}

func appendMissionApprovalEvents(t *testing.T, events *[]Event, mission MissionEnvelope, requester, decider Actor) {
	t.Helper()
	*events = append(*events,
		Event{ID: "EVT-APR-REQ", Type: "approval.requested", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-001", Actor: requester, Payload: mustAgentPayload(t, ApprovalRequested{Approval: ApprovalRequest{ID: "APR-001", SubjectType: "mission", SubjectID: mission.ID, PayloadSHA256: mission.Hash, RiskLevel: mission.RiskLevel, RequesterID: requester.ID}})},
		Event{ID: "EVT-APR-DEC", Type: "approval.decided", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "approval", AggregateID: "APR-001", Actor: decider, Payload: mustAgentPayload(t, ApprovalDecided{ApprovalID: "APR-001", PayloadSHA256: mission.Hash, Decision: "approved", DeciderID: decider.ID})},
	)
}

func missionHashForTest(t *testing.T, envelope MissionEnvelope) string {
	t.Helper()
	envelope.Hash = ""
	canonical, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", digest)
}

func agentTestEvents(t *testing.T, events ...Event) []Event {
	t.Helper()
	payload, err := json.Marshal(ProjectInitialized{Name: "demo", Goal: GoalVersion{Version: 1, Statement: "govern delivery", CompletionCriteria: []string{"complete"}}})
	if err != nil {
		t.Fatal(err)
	}
	initial := Event{ID: "EVT-INIT", Type: "project.initialized", ProjectID: "PRJ-TEST", GoalVersion: 1, AggregateType: "project", AggregateID: "PRJ-TEST", Actor: Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}, OccurredAt: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC), Payload: payload}
	for index := range events {
		events[index].ProjectID = "PRJ-TEST"
		events[index].GoalVersion = 1
		events[index].OccurredAt = initial.OccurredAt.Add(time.Duration(index+1) * time.Minute)
	}
	return append([]Event{initial}, events...)
}

func mustAgentPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

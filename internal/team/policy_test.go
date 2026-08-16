package team

import (
	"errors"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func TestPolicyRequiresPrincipalClaimsToMatchEveryEvent(t *testing.T) {
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-1", "GCH-1")
	event.Sync.DeviceID = "other-device"

	err := (Policy{}).Authorize(testProjectState(t), principal, event, teamTestTime)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() error = %v, want claim authorization error", err)
	}
}

func TestPolicyRejectsAgentApprovalAndSelfApprovalCommands(t *testing.T) {
	principal := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	state := testProjectState(t)
	state.Conflicts["CON-1"] = model.Conflict{ID: "CON-1", Status: "open"}

	tests := []struct {
		name    string
		event   model.Event
		payload any
	}{
		{
			name:    "requirement approval",
			event:   testEventEnvelope(principal, "BATCH-1", 1, "EVT-REQ", "requirement.approved"),
			payload: model.RequirementApproved{RequirementID: "REQ-1"},
		},
		{
			name:    "goal change approval",
			event:   testEventEnvelope(principal, "BATCH-1", 1, "EVT-GCH", "goal.change.approved"),
			payload: model.GoalChangeApproved{GoalChangeID: "GCH-1", DeciderID: principal.Actor.ID, DecidedAt: teamTestTime},
		},
		{
			name:    "evidence verification",
			event:   testEventEnvelope(principal, "BATCH-1", 1, "EVT-EVD", "evidence.verified"),
			payload: model.EvidenceVerified{Evidence: model.Evidence{ID: "EVD-1", TaskID: "TSK-1", RunID: "RUN-1", ContextID: "CTX-1", Kind: "Build", Actor: principal.Actor}},
		},
		{
			name:    "task completion",
			event:   testEventEnvelope(principal, "BATCH-1", 1, "EVT-TSK", "task.completed"),
			payload: model.TaskCompleted{TaskID: "TSK-1"},
		},
		{
			name:    "conflict resolution",
			event:   testEventEnvelope(principal, "BATCH-1", 1, "EVT-CON", "conflict.resolved"),
			payload: model.ConflictResolved{ConflictID: "CON-1", ResolverID: principal.Actor.ID, Resolution: "accept", ResolvedAt: teamTestTime},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.event.Payload = mustJSON(t, test.payload)
			setPayloadDigest(&test.event)
			if err := (Policy{}).Authorize(state, principal, test.event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authorize() error = %v, want agent self-approval rejection", err)
			}
		})
	}
}

func TestPolicyAllowsLeadToIssueScopedLease(t *testing.T) {
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	addCurrentLeaseContext(&state, "TSK-1", "CTX-1")
	lease := model.Lease{
		ID:            "LEASE-1",
		TaskID:        "TSK-1",
		ContextID:     "CTX-1",
		SubjectKind:   string(model.ActorAgent),
		SubjectID:     "AGT-BUILD",
		EnvironmentID: principal.EnvironmentID,
		GoalVersion:   1,
		Revision:      1,
		AllowedScopes: []string{"internal/team"},
		StartsAt:      teamTestTime.Add(-time.Minute),
		ExpiresAt:     teamTestTime.Add(time.Hour),
	}
	event := testEventEnvelope(principal, "BATCH-1", 1, "EVT-LEASE", "lease.issued")
	event.Sync.TaskID = lease.TaskID
	event.Sync.ContextID = lease.ContextID
	event.Payload = mustJSON(t, model.LeaseIssued{Lease: lease})
	setPayloadDigest(&event)

	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); err != nil {
		t.Fatalf("Authorize() error = %v, want lead scoped lease approval", err)
	}

	lease.AllowedScopes = nil
	event.Payload = mustJSON(t, model.LeaseIssued{Lease: lease})
	setPayloadDigest(&event)
	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() error = %v, want unscoped lease rejection", err)
	}
}

func TestPolicyRejectsLeaseIssueWithoutCurrentContext(t *testing.T) {
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	addCurrentLeaseContext(&state, "TSK-1", "CTX-1")
	lease := model.Lease{
		ID: "LEASE-CONTEXT", TaskID: "TSK-1", ContextID: "CTX-1", SubjectKind: string(model.ActorAgent), SubjectID: "AGT-BUILD",
		EnvironmentID: principal.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
	}
	tests := []struct {
		name   string
		mutate func(*model.Lease, *model.SyncMetadata)
	}{
		{
			name: "empty context",
			mutate: func(lease *model.Lease, sync *model.SyncMetadata) {
				lease.ContextID = ""
				sync.ContextID = ""
			},
		},
		{
			name: "unknown context",
			mutate: func(lease *model.Lease, sync *model.SyncMetadata) {
				lease.ContextID = "CTX-MISSING"
				sync.ContextID = lease.ContextID
			},
		},
		{
			name: "context task mismatch",
			mutate: func(lease *model.Lease, sync *model.SyncMetadata) {
				lease.TaskID = "TSK-OTHER"
				sync.TaskID = lease.TaskID
			},
		},
		{
			name: "lease goal mismatch",
			mutate: func(lease *model.Lease, _ *model.SyncMetadata) {
				lease.GoalVersion = 2
			},
		},
		{
			name: "sync task mismatch",
			mutate: func(_ *model.Lease, sync *model.SyncMetadata) {
				sync.TaskID = "TSK-OTHER"
			},
		},
		{
			name: "sync context mismatch",
			mutate: func(_ *model.Lease, sync *model.SyncMetadata) {
				sync.ContextID = "CTX-MISSING"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := lease
			event := testEventEnvelope(principal, "BATCH-CONTEXT-"+test.name, 1, "EVT-CONTEXT-"+test.name, "lease.issued")
			event.Sync.TaskID = candidate.TaskID
			event.Sync.ContextID = candidate.ContextID
			test.mutate(&candidate, event.Sync)
			event.Payload = mustJSON(t, model.LeaseIssued{Lease: candidate})
			setPayloadDigest(&event)

			if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authorize() error = %v, want lease context rejection", err)
			}
		})
	}
}

func TestPolicyRequiresActiveAssignedLeaseForContributorAndAgent(t *testing.T) {
	principal := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	state := testProjectState(t)
	addCurrentLeaseContext(&state, "TSK-1", "CTX-1")
	event := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-1", "GCH-1")
	event.Sync.LeaseID = "LEASE-1"
	event.Sync.TaskID = "TSK-1"
	event.Sync.ContextID = "CTX-1"
	event.Sync.AffectedScope = []string{"internal/team"}
	setPayloadDigest(&event)

	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("Authorize() error = %v, want active assigned lease", err)
	}

	state.Leases["LEASE-1"] = model.Lease{
		ID:            "LEASE-1",
		TaskID:        "TSK-1",
		ContextID:     "CTX-1",
		SubjectKind:   string(model.ActorAgent),
		SubjectID:     principal.Actor.ID,
		EnvironmentID: principal.EnvironmentID,
		GoalVersion:   1,
		Revision:      1,
		Status:        "active",
		AllowedScopes: []string{"internal/team"},
		AllowedSkills: []string{"build"},
		StartsAt:      teamTestTime.Add(-time.Minute),
		ExpiresAt:     teamTestTime.Add(time.Minute),
	}
	event.Sync.SkillName = "build"
	principal.AllowedSkills = []string{"build"}
	setPayloadDigest(&event)
	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); err != nil {
		t.Fatalf("Authorize() error = %v, want assigned lease authorization", err)
	}

	event.Sync.AffectedScope = []string{"outside/lease"}
	setPayloadDigest(&event)
	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("Authorize() error = %v, want scope lease rejection", err)
	}
}

func TestPolicyVerifyFunctionalIdentityCannotVerifyOwnBuildEvidence(t *testing.T) {
	principal := testPrincipal("USR-VERIFY", model.ActorHuman, model.RoleReviewer)
	principal.FunctionalIdentity = "verify"
	state := testProjectState(t)
	addCurrentLeaseContext(&state, "TSK-1", "CTX-1")
	state.Runs["RUN-BUILD"] = model.Run{ID: "RUN-BUILD", ActorID: "AGT-BUILD"}
	event := testEventEnvelope(principal, "BATCH-1", 1, "EVT-VERIFY", "evidence.verified")
	evidence := model.Evidence{ID: "EVD-BUILD", TaskID: "TSK-1", RunID: "RUN-BUILD", ContextID: "CTX-1", Kind: "Build", Actor: model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}}
	event.Payload = mustJSON(t, model.EvidenceVerified{Evidence: evidence})
	setPayloadDigest(&event)

	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); err != nil {
		t.Fatalf("Authorize() error = %v, want independent build verification", err)
	}

	state.Runs["RUN-BUILD"] = model.Run{ID: "RUN-BUILD", ActorID: principal.Actor.ID}
	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() error = %v, want verifier self-run rejection", err)
	}

	state.Runs["RUN-BUILD"] = model.Run{ID: "RUN-BUILD", ActorID: "AGT-BUILD"}
	evidence.Actor = principal.Actor
	event.Payload = mustJSON(t, model.EvidenceVerified{Evidence: evidence})
	setPayloadDigest(&event)
	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() error = %v, want verifier self-evidence rejection", err)
	}
}

func TestPolicyRequiresAssignedLeaseForAgentVerifier(t *testing.T) {
	principal := testPrincipal("AGT-VERIFY", model.ActorAgent, model.RoleAgent)
	principal.FunctionalIdentity = "verify"
	state := testProjectState(t)
	state.Runs["RUN-BUILD"] = model.Run{ID: "RUN-BUILD", ActorID: "AGT-BUILD"}
	event := testEventEnvelope(principal, "BATCH-1", 1, "EVT-VERIFY", "evidence.verified")
	event.Payload = mustJSON(t, model.EvidenceVerified{Evidence: model.Evidence{
		ID: "EVD-BUILD", TaskID: "TSK-1", RunID: "RUN-BUILD", ContextID: "CTX-1", Kind: "Build",
		Actor: model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent},
	}})
	setPayloadDigest(&event)

	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrLeaseRequired) {
		t.Fatalf("Authorize() error = %v, want agent verifier lease rejection", err)
	}
}

func TestPolicyUsesAcceptedEvidenceActorForAgentSelfVerification(t *testing.T) {
	principal := testPrincipal("AGT-VERIFY", model.ActorAgent, model.RoleAgent)
	principal.FunctionalIdentity = "verify"
	state := testProjectState(t)
	state.Runs["RUN-BUILD"] = model.Run{ID: "RUN-BUILD", ActorID: "AGT-BUILD"}
	state.Evidence["TSK-1"] = []model.Evidence{{
		ID: "EVD-OWN", TaskID: "TSK-1", RunID: "RUN-BUILD", ContextID: "CTX-1", Kind: "Build", Status: "candidate", Actor: principal.Actor,
	}}
	state.Leases["LEASE-VERIFY"] = model.Lease{
		ID: "LEASE-VERIFY", TaskID: "TSK-1", ContextID: "CTX-1", SubjectKind: string(model.ActorAgent), SubjectID: principal.Actor.ID,
		EnvironmentID: principal.EnvironmentID, GoalVersion: 1, Revision: 1, Status: "active", AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Minute),
	}
	event := testEventEnvelope(principal, "BATCH-1", 1, "EVT-VERIFY", "evidence.verified")
	event.Sync.LeaseID = "LEASE-VERIFY"
	event.Sync.TaskID = "TSK-1"
	event.Sync.ContextID = "CTX-1"
	event.Sync.AffectedScope = []string{"internal/team"}
	// The event payload lies about the evidence actor. Policy must use the
	// accepted candidate record rather than this mutable verification payload.
	event.Payload = mustJSON(t, model.EvidenceVerified{Evidence: model.Evidence{
		ID: "EVD-OWN", TaskID: "TSK-1", RunID: "RUN-BUILD", ContextID: "CTX-1", Kind: "Build",
		Actor: model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent},
	}})
	setPayloadDigest(&event)

	if err := (Policy{}).Authorize(state, principal, event, teamTestTime); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Authorize() error = %v, want accepted self-evidence rejection", err)
	}
}

func addCurrentLeaseContext(state *model.ProjectState, taskID, contextID string) {
	state.Tasks[taskID] = model.Task{ID: taskID, GoalVersion: state.Goal.Version}
	state.Contexts[contextID] = model.ContextSlice{ID: contextID, TaskID: taskID, GoalVersion: state.Goal.Version, SliceHash: "slice-" + contextID}
}

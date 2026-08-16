package team

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

var teamTestTime = time.Date(2026, 8, 9, 2, 3, 4, 0, time.UTC)

func TestPushAcceptsBatchOnceAndReturnsOriginalTeamSequenceOnRetry(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-1", "GCH-1")
	batch := PushBatch{BatchID: "BATCH-1", BaseTeamSeq: 1, Events: []model.Event{event}}

	first, err := service.Push(ctx, principal, batch)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != PushAccepted || first.TeamSeqFrom != 2 || first.TeamSeqTo != 2 || !first.Materialized {
		t.Fatalf("first Push() = %#v, want accepted materialized sequence 2", first)
	}

	retry, err := service.Push(ctx, principal, batch)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != PushAccepted || retry.TeamSeqFrom != first.TeamSeqFrom || retry.TeamSeqTo != first.TeamSeqTo || !retry.Materialized {
		t.Fatalf("retry Push() = %#v, want original accepted range %#v", retry, first)
	}

	accepted := readTeamAccepted(t, root)
	materialized := readMaterialized(t, root)
	if len(accepted) != 2 || !reflect.DeepEqual(materialized, accepted) {
		t.Fatalf("accepted=%#v materialized=%#v, want one exact two-event chain", accepted, materialized)
	}
	if accepted[1].Sequence != retry.Events[0].Sequence || accepted[1].Sequence != 2 {
		t.Fatalf("TeamSeq must equal accepted event sequence: accepted=%d retry=%d", accepted[1].Sequence, retry.Events[0].Sequence)
	}
}

func TestPushRejectsNewEventsForAcceptedBatchID(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	first := testGoalProposalEvent(t, principal, "BATCH-REUSED", 1, "EVT-ORIGINAL", "GCH-ORIGINAL")
	firstResult, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-REUSED", BaseTeamSeq: 1, Events: []model.Event{first}})
	if err != nil || firstResult.Status != PushAccepted {
		t.Fatalf("first Push() = %#v, %v", firstResult, err)
	}

	replacement := testGoalProposalEvent(t, principal, "BATCH-REUSED", 2, "EVT-REPLACEMENT", "GCH-REPLACEMENT")
	result, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-REUSED", BaseTeamSeq: 2, Events: []model.Event{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "batch_conflict" {
		t.Fatalf("Push() = %#v, want accepted-batch conflict", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 2 {
		t.Fatalf("accepted event count = %d, want no partial append after BatchID conflict", len(accepted))
	}
	if accepted, materialized := readTeamAccepted(t, root), readMaterialized(t, root); !reflect.DeepEqual(materialized, accepted) {
		t.Fatalf("materialized history = %#v, want unchanged accepted history %#v", materialized, accepted)
	}
}

func TestPushRetryReturnsAcceptedOrderWhenCallerReordersAnAcceptedBatch(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	proposal := testGoalProposalEvent(t, principal, "BATCH-TWO", 1, "EVT-PROPOSAL", "GCH-TWO")
	approval := testEventEnvelope(principal, "BATCH-TWO", 1, "EVT-APPROVAL", "goal.change.approved")
	approval.AggregateType = "goal_change"
	approval.AggregateID = "GCH-TWO"
	approval.Payload = mustJSON(t, model.GoalChangeApproved{GoalChangeID: "GCH-TWO", DeciderID: principal.Actor.ID, DecidedAt: teamTestTime})
	setPayloadDigest(&approval)
	batch := PushBatch{BatchID: "BATCH-TWO", BaseTeamSeq: 1, Events: []model.Event{proposal, approval}}

	first, err := service.Push(ctx, principal, batch)
	if err != nil || first.Status != PushAccepted {
		t.Fatalf("first Push() = %#v, %v", first, err)
	}
	reordered := PushBatch{BatchID: batch.BatchID, BaseTeamSeq: batch.BaseTeamSeq, Events: []model.Event{approval, proposal}}
	retry, err := service.Push(ctx, principal, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != PushAccepted || retry.TeamSeqFrom != first.TeamSeqFrom || retry.TeamSeqTo != first.TeamSeqTo {
		t.Fatalf("reordered retry = %#v, want original range %#v", retry, first)
	}
	if len(retry.Events) != 2 || retry.Events[0].Sequence != first.TeamSeqFrom || retry.Events[1].Sequence != first.TeamSeqTo {
		t.Fatalf("reordered retry events = %#v, want accepted sequence order", retry.Events)
	}
}

func TestPushPreviewsEntireBatchWithoutPartialAcceptance(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	first := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-1", "GCH-1")
	second := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-2", "GCH-1")

	result, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-1", BaseTeamSeq: 1, Events: []model.Event{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected {
		t.Fatalf("Push() status = %q, want %q", result.Status, PushRejected)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 1 {
		t.Fatalf("accepted event count = %d, want no partial batch append", len(accepted))
	}
}

func TestPushRejectsCandidateInvalidAtBase(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-STALE", 0, "EVT-GCH-1", "GCH-1")

	result, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-STALE", BaseTeamSeq: 0, Events: []model.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "invalid_goal" {
		t.Fatalf("Push() = %#v, want base-invalid candidate rejection", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 1 {
		t.Fatalf("accepted event count = %d, want 1 after conflict", len(accepted))
	}
}

func TestPushRejectsUnknownContextBeforeAcceptance(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-CONTEXT", 1, "EVT-GCH-CONTEXT", "GCH-CONTEXT")
	event.Sync.ContextID = "CTX-MISSING"
	event.Sync.TaskID = "TSK-1"
	setPayloadDigest(&event)

	result, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-CONTEXT", BaseTeamSeq: 1, Events: []model.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "invalid_context" {
		t.Fatalf("Push() = %#v, want unknown-context rejection", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 1 {
		t.Fatalf("accepted event count = %d, want no append for an unknown context", len(accepted))
	}
}

func TestPushRetryReturnsOriginalSequenceAfterGoalChangeStalesAgentLease(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	seedVerifyingTaskContext(t, root, "TSK-1", "CTX-1", "RUN-1")
	lease := model.Lease{
		ID:            "LEASE-1",
		TaskID:        "TSK-1",
		ContextID:     "CTX-1",
		SubjectKind:   string(model.ActorAgent),
		SubjectID:     agent.Actor.ID,
		EnvironmentID: agent.EnvironmentID,
		GoalVersion:   1,
		Revision:      1,
		AllowedScopes: []string{"internal/team"},
		StartsAt:      teamTestTime.Add(-time.Minute),
		ExpiresAt:     teamTestTime.Add(time.Hour),
	}
	if result, err := service.IssueLease(ctx, owner, lease); err != nil || result.Status != PushAccepted {
		t.Fatalf("IssueLease() = %#v, %v", result, err)
	}
	base := uint64(len(readTeamAccepted(t, root)))
	event := testGoalProposalEvent(t, agent, "BATCH-AGENT", base, "EVT-GCH-AGENT", "GCH-AGENT")
	event.Sync.LeaseID = lease.ID
	event.Sync.TaskID = lease.TaskID
	event.Sync.ContextID = lease.ContextID
	event.Sync.AffectedScope = []string{"internal/team"}
	setPayloadDigest(&event)
	batch := PushBatch{BatchID: "BATCH-AGENT", BaseTeamSeq: base, Events: []model.Event{event}}
	first, err := service.Push(ctx, agent, batch)
	if err != nil || first.Status != PushAccepted {
		t.Fatalf("first Push() = %#v, %v", first, err)
	}
	if result, err := service.ApproveGoalChange(ctx, owner, "GCH-AGENT"); err != nil || result.Status != PushAccepted {
		t.Fatalf("ApproveGoalChange() = %#v, %v", result, err)
	}

	retry, err := service.Push(ctx, agent, batch)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != PushAccepted || retry.TeamSeqFrom != first.TeamSeqFrom || retry.TeamSeqTo != first.TeamSeqTo {
		t.Fatalf("retry Push() = %#v, want original accepted range %#v", retry, first)
	}
}

func TestPushRejectsAgentBatchWithoutClaimedLeaseScope(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	seedVerifyingTaskContext(t, root, "TSK-1", "CTX-1", "RUN-1")
	lease := model.Lease{
		ID: "LEASE-SCOPED", TaskID: "TSK-1", ContextID: "CTX-1", SubjectKind: string(model.ActorAgent), SubjectID: agent.Actor.ID,
		EnvironmentID: agent.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
	}
	if result, err := service.IssueLease(ctx, owner, lease); err != nil || result.Status != PushAccepted {
		t.Fatalf("IssueLease() = %#v, %v", result, err)
	}
	base := uint64(len(readTeamAccepted(t, root)))
	event := testGoalProposalEvent(t, agent, "BATCH-NO-SCOPE", base, "EVT-NO-SCOPE", "GCH-NO-SCOPE")
	event.Sync.LeaseID = lease.ID
	event.Sync.TaskID = lease.TaskID
	event.Sync.ContextID = lease.ContextID
	setPayloadDigest(&event)

	acceptedCount := len(readTeamAccepted(t, root))
	result, err := service.Push(ctx, agent, PushBatch{BatchID: "BATCH-NO-SCOPE", BaseTeamSeq: base, Events: []model.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "unauthorized" {
		t.Fatalf("Push() = %#v, want unscoped agent batch rejection", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != acceptedCount {
		t.Fatalf("accepted event count = %d, want no append for an unscoped batch", len(accepted))
	}
}

func TestPushRejectsAgentBatchOutsideAssignedLeaseCoordinates(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	seedVerifyingTaskContext(t, root, "TSK-LEASE", "CTX-LEASE", "RUN-LEASE")
	lease := model.Lease{
		ID: "LEASE-COORDINATES", TaskID: "TSK-LEASE", ContextID: "CTX-LEASE", SubjectKind: string(model.ActorAgent), SubjectID: agent.Actor.ID,
		EnvironmentID: agent.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
	}
	if result, err := service.IssueLease(ctx, owner, lease); err != nil || result.Status != PushAccepted {
		t.Fatalf("IssueLease() = %#v, %v", result, err)
	}

	tests := []struct {
		name      string
		principal Principal
		mutate    func(*model.Event)
	}{
		{
			name:      "different task",
			principal: agent,
			mutate: func(event *model.Event) {
				event.Sync.TaskID = "TSK-OTHER"
			},
		},
		{
			name:      "empty context",
			principal: agent,
			mutate: func(event *model.Event) {
				event.Sync.ContextID = ""
			},
		},
		{
			name:      "different context",
			principal: agent,
			mutate: func(event *model.Event) {
				event.Sync.ContextID = "CTX-OTHER"
			},
		},
		{
			name: "different environment",
			principal: func() Principal {
				other := agent
				other.EnvironmentID = "other-environment"
				return other
			}(),
			mutate: func(*model.Event) {},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := uint64(len(readTeamAccepted(t, root)))
			event := testGoalProposalEvent(t, test.principal, fmt.Sprintf("BATCH-COORDINATE-%d", index), base, fmt.Sprintf("EVT-COORDINATE-%d", index), fmt.Sprintf("GCH-COORDINATE-%d", index))
			event.Sync.LeaseID = lease.ID
			event.Sync.TaskID = lease.TaskID
			event.Sync.ContextID = lease.ContextID
			event.Sync.AffectedScope = []string{"internal/team"}
			test.mutate(&event)
			setPayloadDigest(&event)

			result, err := service.Push(ctx, test.principal, PushBatch{BatchID: event.Sync.BatchID, BaseTeamSeq: base, Events: []model.Event{event}})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != PushRejected || result.Code != "unauthorized" {
				t.Fatalf("Push() = %#v, want lease-coordinate rejection", result)
			}
		})
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 7 {
		t.Fatalf("accepted event count = %d, want setup facts and one lease only", len(accepted))
	}
}

func TestPushRejectsAgentBatchWhenLegacyLeaseHasNoContext(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	seedVerifyingTaskContext(t, root, "TSK-LEGACY", "CTX-LEGACY", "RUN-LEGACY")
	legacyLease := model.Lease{
		ID: "LEASE-LEGACY", TaskID: "TSK-LEGACY", SubjectKind: string(model.ActorAgent), SubjectID: agent.Actor.ID,
		EnvironmentID: agent.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
	}
	appendMirroredTeamEvent(t, root, testSeedEvent(t, "EVT-LEGACY-LEASE", "lease.issued", model.LeaseIssued{Lease: legacyLease}))

	base := uint64(len(readTeamAccepted(t, root)))
	event := testGoalProposalEvent(t, agent, "BATCH-LEGACY", base, "EVT-LEGACY-PUSH", "GCH-LEGACY")
	event.Sync.LeaseID = legacyLease.ID
	event.Sync.TaskID = legacyLease.TaskID
	event.Sync.AffectedScope = []string{"internal/team"}
	setPayloadDigest(&event)

	result, err := service.Push(ctx, agent, PushBatch{BatchID: "BATCH-LEGACY", BaseTeamSeq: base, Events: []model.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "unauthorized" {
		t.Fatalf("Push() = %#v, want legacy empty-context lease rejection", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != int(base) {
		t.Fatalf("accepted event count = %d, want no legacy lease batch append", len(accepted))
	}
}

func TestIssueLeaseRejectsEmptyOrWrongContext(t *testing.T) {
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	tests := []struct {
		name   string
		mutate func(*model.Lease)
	}{
		{
			name: "empty context",
			mutate: func(lease *model.Lease) {
				lease.ContextID = ""
			},
		},
		{
			name: "unknown context",
			mutate: func(lease *model.Lease) {
				lease.ContextID = "CTX-MISSING"
			},
		},
		{
			name: "context task mismatch",
			mutate: func(lease *model.Lease) {
				lease.TaskID = "TSK-OTHER"
			},
		},
		{
			name: "goal mismatch",
			mutate: func(lease *model.Lease) {
				lease.GoalVersion = 2
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			root := newInitializedTeamRoot(t)
			service := newTeamService(t, root, Dependencies{})
			seedVerifyingTaskContext(t, root, "TSK-1", "CTX-1", "RUN-1")
			lease := model.Lease{
				ID: "LEASE-" + test.name, TaskID: "TSK-1", ContextID: "CTX-1", SubjectKind: string(model.ActorAgent), SubjectID: agent.Actor.ID,
				EnvironmentID: agent.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
				StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
			}
			test.mutate(&lease)

			result, err := service.IssueLease(ctx, owner, lease)
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != PushRejected || result.Code != "unauthorized" {
				t.Fatalf("IssueLease() = %#v, want context authorization rejection", result)
			}
			if accepted := readTeamAccepted(t, root); len(accepted) != 6 {
				t.Fatalf("accepted event count = %d, want setup facts only", len(accepted))
			}
		})
	}
}

func TestPushRejectsAgentSelfVerificationOfCandidateCreatedEarlierInBatch(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	seedVerifyingTaskContext(t, root, "TSK-SELF", "CTX-SELF", "RUN-SELF")
	lease := model.Lease{
		ID: "LEASE-SELF", TaskID: "TSK-SELF", ContextID: "CTX-SELF", SubjectKind: string(model.ActorAgent), SubjectID: agent.Actor.ID,
		EnvironmentID: agent.EnvironmentID, GoalVersion: 1, Revision: 1, AllowedScopes: []string{"internal/team"},
		StartsAt: teamTestTime.Add(-time.Minute), ExpiresAt: teamTestTime.Add(time.Hour),
	}
	if result, err := service.IssueLease(ctx, owner, lease); err != nil || result.Status != PushAccepted {
		t.Fatalf("IssueLease() = %#v, %v", result, err)
	}

	base := uint64(len(readTeamAccepted(t, root)))
	evidence := model.Evidence{
		ID: "EVD-SAME-BATCH", TaskID: lease.TaskID, RunID: "RUN-SELF", ContextID: lease.ContextID, GoalVersion: 1,
		Kind: "Build", URI: "artifacts/self.log", SHA256: "self-evidence-hash",
	}
	candidate := testEventEnvelope(agent, "BATCH-SELF-VERIFY", base, "EVT-CANDIDATE", "evidence.candidate.recorded")
	candidate.AggregateType = "evidence"
	candidate.AggregateID = evidence.ID
	candidate.Payload = mustJSON(t, model.EvidenceCandidateRecorded{Evidence: evidence})
	candidate.Sync.LeaseID = lease.ID
	candidate.Sync.TaskID = lease.TaskID
	candidate.Sync.ContextID = lease.ContextID
	candidate.Sync.AffectedScope = []string{"internal/team"}
	setPayloadDigest(&candidate)

	forged := evidence
	forged.Actor = model.Actor{ID: "AGT-OTHER", Kind: model.ActorAgent, Role: model.RoleAgent}
	verified := testEventEnvelope(agent, "BATCH-SELF-VERIFY", base, "EVT-VERIFIED", "evidence.verified")
	verified.AggregateType = "evidence"
	verified.AggregateID = evidence.ID
	verified.Payload = mustJSON(t, model.EvidenceVerified{Evidence: forged})
	verified.Sync.LeaseID = lease.ID
	verified.Sync.TaskID = lease.TaskID
	verified.Sync.ContextID = lease.ContextID
	verified.Sync.AffectedScope = []string{"internal/team"}
	setPayloadDigest(&verified)

	result, err := service.Push(ctx, agent, PushBatch{BatchID: "BATCH-SELF-VERIFY", BaseTeamSeq: base, Events: []model.Event{candidate, verified}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushRejected || result.Code != "unauthorized" {
		t.Fatalf("Push() = %#v, want same-batch self-verification rejection", result)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != int(base) {
		t.Fatalf("accepted event count = %d, want no partial same-batch append", len(accepted))
	}
}

func TestPushReportsAcceptedWhenMaterializationFailsAndRecoverRepairsOnRestart(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	failing := &switchableMaterializer{delegate: NewFileMaterializer(root, eventstore.New(root), nil)}
	service := newTeamService(t, root, Dependencies{Materializer: failing})
	failing.fail = true
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-1", "GCH-1")

	result, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-1", BaseTeamSeq: 1, Events: []model.Event{event}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PushAccepted || result.Materialized {
		t.Fatalf("Push() = %#v, want accepted but unmaterialized result", result)
	}
	if len(readTeamAccepted(t, root)) != 2 || len(readMaterialized(t, root)) != 1 {
		t.Fatal("accepted log and canonical log were not left in recoverable states")
	}

	failing.fail = false
	restarted := newTeamService(t, root, Dependencies{})
	status, err := restarted.Status(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Writable || status.MaterializedThrough != 2 {
		t.Fatalf("Status() = %#v, want writable recovery through sequence 2", status)
	}
	if accepted, materialized := readTeamAccepted(t, root), readMaterialized(t, root); !reflect.DeepEqual(materialized, accepted) {
		t.Fatalf("recovered materialized log = %#v, want accepted log %#v", materialized, accepted)
	}
}

func TestOpenSeedsEmptyAcceptedLogAndRefusesDivergentHistory(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	if len(readTeamAccepted(t, root)) != 1 {
		t.Fatal("New() did not seed accepted history from canonical facts")
	}
	if _, err := service.Status(ctx, testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)); err != nil {
		t.Fatal(err)
	}

	divergentRoot := newInitializedTeamRoot(t)
	if err := ensureAcceptedLog(divergentRoot); err != nil {
		t.Fatal(err)
	}
	acceptedStore := teamAcceptedStore(divergentRoot)
	if _, err := acceptedStore.Append(ctx, model.Event{
		ID: "EVT-DIVERGENT", Type: "project.initialized", ProjectID: "PRJ-TEST", GoalVersion: 1,
		AggregateType: "project", AggregateID: "PRJ-TEST", Actor: model.Actor{ID: "USR-OTHER", Kind: model.ActorHuman, Role: model.RoleOwner}, OccurredAt: teamTestTime,
		Payload: mustJSON(t, model.ProjectInitialized{Name: "other", Goal: model.GoalVersion{Version: 1, Statement: "Other", CompletionCriteria: []string{"other"}}}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx, divergentRoot, testDependencies()); !errors.Is(err, ErrHistoryDivergent) {
		t.Fatalf("New() error = %v, want divergent accepted/canonical history refusal", err)
	}
}

func TestGoalApprovalMaterializesAndAtomicallyUpdatesManifest(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)

	proposal, err := service.ProposeGoalChange(ctx, principal, model.GoalChange{
		ID:          "GCH-2",
		Reason:      "Add team synchronization",
		BaseVersion: 1,
		Proposed:    model.GoalVersion{Version: 2, Statement: "Coordinate a team", CompletionCriteria: []string{"team state is materialized"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status != PushAccepted || !proposal.Materialized {
		t.Fatalf("ProposeGoalChange() = %#v", proposal)
	}
	approval, err := service.ApproveGoalChange(ctx, principal, "GCH-2")
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != PushAccepted || !approval.Materialized {
		t.Fatalf("ApproveGoalChange() = %#v", approval)
	}
	manifest, err := capsule.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.CurrentGoalVersion != 2 {
		t.Fatalf("manifest goal version = %d, want 2 after accepted approval materializes", manifest.CurrentGoalVersion)
	}
}

func seedVerifyingTaskContext(t *testing.T, root, taskID, contextID, runID string) {
	t.Helper()
	requirementID := "REQ-" + taskID
	sliceHash := "slice-" + contextID
	events := []model.Event{
		testSeedEvent(t, "EVT-SEED-"+taskID+"-REQUIREMENT", "requirement.planned", model.RequirementPlanned{
			Requirement: model.Requirement{ID: requirementID, GoalVersion: 1, Title: "Seed current context"},
			Tasks:       []model.Task{{ID: taskID, RequirementID: requirementID, GoalVersion: 1, Title: "Seed task"}},
		}),
		testSeedEvent(t, "EVT-SEED-"+taskID+"-APPROVED", "requirement.approved", model.RequirementApproved{RequirementID: requirementID}),
		testSeedEvent(t, "EVT-SEED-"+taskID+"-CONTEXT", "context.issued", model.ContextIssued{Context: model.ContextSlice{
			ID: contextID, TaskID: taskID, GoalVersion: 1, Summary: "Seed current context", SliceHash: sliceHash,
		}}),
		testSeedEvent(t, "EVT-SEED-"+taskID+"-RUN", "run.started", model.RunStarted{Run: model.Run{
			ID: runID, TaskID: taskID, GoalVersion: 1, Executor: "seed", ActorID: "AGT-OTHER", ContextID: contextID, ContextHash: sliceHash,
		}}),
		testSeedEvent(t, "EVT-SEED-"+taskID+"-FINISHED", "run.finished", model.RunFinished{RunID: runID, Result: "seeded"}),
	}
	for _, event := range events {
		appendMirroredTeamEvent(t, root, event)
	}
}

func appendMirroredTeamEvent(t *testing.T, root string, event model.Event) {
	t.Helper()
	canonical, err := eventstore.New(root).Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := teamAcceptedStore(root).Append(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accepted, canonical) {
		t.Fatalf("mirrored accepted event = %#v, want canonical event %#v", accepted, canonical)
	}
}

func testSeedEvent(t *testing.T, eventID, eventType string, payload any) model.Event {
	t.Helper()
	return model.Event{
		ID:            eventID,
		Type:          eventType,
		ProjectID:     "PRJ-TEST",
		GoalVersion:   1,
		AggregateType: "seed",
		AggregateID:   eventID,
		Actor:         model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
		OccurredAt:    teamTestTime,
		Payload:       mustJSON(t, payload),
	}
}

func testPrincipal(id string, kind model.ActorKind, role model.ActorRole) Principal {
	return Principal{
		AuthenticatedPrincipal: "auth-" + id,
		Actor:                  model.Actor{ID: id, Kind: kind, Role: role},
		DeviceID:               "device-" + id,
		EnvironmentID:          "team-test",
	}
}

func testProjectState(t *testing.T) model.ProjectState {
	t.Helper()
	state, err := model.Reduce([]model.Event{testInitializedEvent(t)})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func testInitializedEvent(t *testing.T) model.Event {
	t.Helper()
	return model.Event{
		ID:            "EVT-INIT",
		Type:          "project.initialized",
		ProjectID:     "PRJ-TEST",
		GoalVersion:   1,
		AggregateType: "project",
		AggregateID:   "PRJ-TEST",
		Actor:         model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
		OccurredAt:    teamTestTime,
		Payload: mustJSON(t, model.ProjectInitialized{
			Name: "team test",
			Goal: model.GoalVersion{Version: 1, Statement: "Keep work auditable", CompletionCriteria: []string{"tests pass"}},
		}),
	}
}

func testGoalProposalEvent(t *testing.T, principal Principal, batchID string, base uint64, eventID, changeID string) model.Event {
	t.Helper()
	change := model.GoalChange{
		ID:          changeID,
		Reason:      "Test a Team Core proposal",
		ProposerID:  principal.Actor.ID,
		BaseVersion: 1,
		Proposed:    model.GoalVersion{Version: 2, Statement: "Coordinate team work", CompletionCriteria: []string{"team state stays auditable"}},
		CreatedAt:   teamTestTime,
	}
	event := testEventEnvelope(principal, batchID, base, eventID, "goal.change.proposed")
	event.AggregateType = "goal_change"
	event.AggregateID = changeID
	event.Payload = mustJSON(t, model.GoalChangeProposed{GoalChange: change})
	setPayloadDigest(&event)
	return event
}

func testEventEnvelope(principal Principal, batchID string, base uint64, eventID, eventType string) model.Event {
	return model.Event{
		ID:            eventID,
		Type:          eventType,
		ProjectID:     "PRJ-TEST",
		GoalVersion:   1,
		AggregateType: "project",
		AggregateID:   "PRJ-TEST",
		Actor:         principal.Actor,
		OccurredAt:    teamTestTime,
		Sync: &model.SyncMetadata{
			DeviceID:               principal.DeviceID,
			AuthenticatedPrincipal: principal.AuthenticatedPrincipal,
			FunctionalIdentity:     principal.FunctionalIdentity,
			EnvironmentID:          principal.EnvironmentID,
			BaseTeamSeq:            base,
			BatchID:                batchID,
			TraceID:                "trace-" + batchID,
		},
	}
}

func setPayloadDigest(event *model.Event) {
	sum := sha256.Sum256(event.Payload)
	event.Sync.PayloadSHA256 = hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func newInitializedTeamRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := capsule.Init(root, capsule.InitInput{ProjectID: "PRJ-TEST", Name: "team test", ActorID: "USR-OWNER", CreatedAt: teamTestTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := eventstore.New(root).Append(context.Background(), testInitializedEvent(t)); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTeamService(t *testing.T, root string, dependencies Dependencies) *Service {
	t.Helper()
	if dependencies.Clock == nil {
		dependencies.Clock = fixedTeamClock{value: teamTestTime}
	}
	if dependencies.IDs == nil {
		dependencies.IDs = &teamIDs{}
	}
	service, err := New(context.Background(), root, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func teamAcceptedStore(root string) eventstore.Store {
	return eventstore.NewAt(root+"/.haowork/team/events.jsonl", root+"/.haowork/team/events.lock")
}

func readTeamAccepted(t *testing.T, root string) []model.Event {
	t.Helper()
	events, err := teamAcceptedStore(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func readMaterialized(t *testing.T, root string) []model.Event {
	t.Helper()
	events, err := eventstore.New(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return events
}

type fixedTeamClock struct{ value time.Time }

func (clock fixedTeamClock) Now() time.Time { return clock.value }

type teamIDs struct {
	mu sync.Mutex
	n  int
}

func (ids *teamIDs) New(prefix string) (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.n++
	return fmt.Sprintf("%s-%03d", prefix, ids.n), nil
}

package team

import (
	"context"
	"fmt"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestDetectConflict(t *testing.T) {
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)

	tests := []struct {
		name          string
		mutate        func(*model.ProjectState, *PushBatch)
		want          string
		acceptedScope string
		acceptedEvent func() model.Event
	}{
		{
			name: "stale goal",
			mutate: func(state *model.ProjectState, batch *PushBatch) {
				state.Goal.Version = 2
				batch.Events[0].GoalVersion = 1
			},
			want:          "stale_goal",
			acceptedScope: "internal/team/service.go",
		},
		{
			name: "lease reassigned",
			mutate: func(state *model.ProjectState, batch *PushBatch) {
				state.Leases["LEASE-1"] = model.Lease{ID: "LEASE-1", SubjectID: "USR-OTHER", Status: "active"}
				batch.Events[0].Sync.LeaseID = "LEASE-1"
			},
			want:          "lease_reassigned",
			acceptedScope: "internal/team/service.go",
		},
		{
			name: "scope overlap",
			mutate: func(_ *model.ProjectState, batch *PushBatch) {
				batch.Events[0].Sync.AffectedScope = []string{"internal/team/service.go"}
				batch.Events[0].Sync.BaseTeamSeq = 1
				batch.BaseTeamSeq = 1
			},
			want:          "scope_overlap",
			acceptedScope: "internal/team/service.go",
		},
		{
			name: "design diverged",
			mutate: func(_ *model.ProjectState, batch *PushBatch) {
				batch.Events[0] = designConflictEvent(t, "EVT-DESIGN-LOCAL", "design:api-v1", "local contract")
			},
			want: "design_diverged",
			acceptedEvent: func() model.Event {
				return designConflictEvent(t, "EVT-DESIGN-TEAM", "design:api-v1", "team contract")
			},
		},
		{
			name: "evidence mismatch",
			mutate: func(_ *model.ProjectState, batch *PushBatch) {
				batch.Events[0] = evidenceConflictEvent(t, "EVT-EVIDENCE-LOCAL", "evidence:EVD-1", "sha-local", "base-1")
			},
			want: "evidence_mismatch",
			acceptedEvent: func() model.Event {
				return evidenceConflictEvent(t, "EVT-EVIDENCE-TEAM", "evidence:EVD-1", "sha-team", "base-1")
			},
		},
		{
			name: "terminal state",
			mutate: func(state *model.ProjectState, batch *PushBatch) {
				state.Tasks["TSK-1"] = model.Task{ID: "TSK-1", Status: model.StatusCompleted}
				batch.Events[0].Sync.TaskID = "TSK-1"
			},
			want:          "terminal_state",
			acceptedScope: "internal/team/service.go",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := testProjectState(t)
			batch := PushBatch{BatchID: "BATCH-" + test.name, Events: []model.Event{testEventEnvelope(principal, "BATCH-"+test.name, 0, "EVT-"+test.name, "goal.change.proposed")}}
			test.mutate(&current, &batch)
			accepted := acceptedScopeEvent(t, test.acceptedScope)
			if test.acceptedEvent != nil {
				accepted = test.acceptedEvent()
			}
			conflict, err := DetectConflict(context.Background(), current, principal, batch, []model.Event{accepted}, teamTestTime)
			if err != nil {
				t.Fatal(err)
			}
			if conflict == nil || conflict.Type != test.want {
				t.Fatalf("DetectConflict() = %#v, want type %q", conflict, test.want)
			}
		})
	}
}

func TestDisjointScope(t *testing.T) {
	got, err := NormalizeConflictScopes([]string{"internal\\team\\conflict.go", "module:Team"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "internal/team/conflict.go" || got[1] != "module:Team" {
		t.Fatalf("NormalizeConflictScopes() = %#v", got)
	}
}

func TestDisjointScopeOldBaseIsAccepted(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	lead := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	accepted := testGoalProposalEvent(t, lead, "BATCH-LEFT", 1, "EVT-LEFT", "GCH-LEFT")
	accepted.Sync.AffectedScope = []string{"internal/team/left.go"}
	setPayloadDigest(&accepted)
	if result, err := service.Push(ctx, lead, PushBatch{BatchID: accepted.Sync.BatchID, BaseTeamSeq: 1, Events: []model.Event{accepted}}); err != nil || result.Status != PushAccepted {
		t.Fatalf("seed Push() = %#v, %v", result, err)
	}
	candidate := testGoalProposalEvent(t, lead, "BATCH-RIGHT", 1, "EVT-RIGHT", "GCH-RIGHT")
	candidate.Sync.AffectedScope = []string{"internal/team/right.go"}
	setPayloadDigest(&candidate)
	result, err := service.Push(ctx, lead, PushBatch{BatchID: candidate.Sync.BatchID, BaseTeamSeq: 1, Events: []model.Event{candidate}})
	if err != nil || result.Status != PushAccepted {
		t.Fatalf("disjoint old-base Push() = %#v, %v", result, err)
	}
}

func TestInvalidAtBase(t *testing.T) {
	for _, scope := range []string{"../secret", "a/../secret", "C:/absolute/path", "/absolute/path"} {
		if _, err := NormalizeConflictScopes([]string{scope}); err == nil {
			t.Fatalf("NormalizeConflictScopes(%q) succeeded, want rejection", scope)
		}
	}
}

func TestEvidenceContentDiffersOnlyWhenTypedContentDiffers(t *testing.T) {
	accepted := evidenceConflictEvent(t, "EVT-EVIDENCE-TEAM", "evidence:EVD-1", "sha-1", "base-1")
	identical := evidenceConflictEvent(t, "EVT-EVIDENCE-LOCAL", "evidence:EVD-1", "sha-1", "base-1")
	if evidenceContentDiffers([]model.Event{identical}, []model.Event{accepted}) {
		t.Fatal("identical typed evidence was classified as different")
	}
	changed := evidenceConflictEvent(t, "EVT-EVIDENCE-LOCAL", "evidence:EVD-1", "sha-2", "base-1")
	if !evidenceContentDiffers([]model.Event{changed}, []model.Event{accepted}) {
		t.Fatal("changed typed evidence was not classified as different")
	}
}

func TestDesignConflictRequiresDifferentTypedContent(t *testing.T) {
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	accepted := designConflictEvent(t, "EVT-DESIGN-TEAM", "design:api-v1", "same contract")
	identical := designConflictEvent(t, "EVT-DESIGN-LOCAL", "design:api-v1", "same contract")
	batch := PushBatch{BatchID: "BATCH-DESIGN", Events: []model.Event{identical}}
	conflict, err := DetectConflict(context.Background(), state, principal, batch, []model.Event{accepted}, teamTestTime)
	if err != nil || conflict != nil {
		t.Fatalf("identical design DetectConflict() = %#v, %v", conflict, err)
	}
	changed := designConflictEvent(t, "EVT-DESIGN-LOCAL", "design:api-v1", "changed contract")
	batch.Events = []model.Event{changed}
	conflict, err = DetectConflict(context.Background(), state, principal, batch, []model.Event{accepted}, teamTestTime)
	if err != nil || conflict == nil || conflict.Type != ConflictDesignDiverged {
		t.Fatalf("changed design DetectConflict() = %#v, %v", conflict, err)
	}
}

func TestDesignConflictNormalizesDesignIdentity(t *testing.T) {
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	accepted := designConflictEvent(t, "EVT-DESIGN-TEAM", "design:api", "team contract")
	local := designConflictEvent(t, "EVT-DESIGN-LOCAL", " design:api ", "local contract")
	conflict, err := DetectConflict(context.Background(), state, principal, PushBatch{BatchID: "BATCH-DESIGN", Events: []model.Event{local}}, []model.Event{accepted}, teamTestTime)
	if err != nil || conflict == nil || conflict.Type != ConflictDesignDiverged {
		t.Fatalf("normalized design DetectConflict() = %#v, %v", conflict, err)
	}
}

func TestEvidenceConflictUsesNormalizedDeclaredScope(t *testing.T) {
	accepted := evidenceConflictEventWithPayloadID(t, "EVT-EVIDENCE-TEAM", "evidence:EV-A", "EV-A", "sha-1", "base-1")
	wrongIdentity := evidenceConflictEventWithPayloadID(t, "EVT-EVIDENCE-LOCAL", " evidence:EV-A ", "EV-B", "sha-1", "base-1")
	if !evidenceContentDiffers([]model.Event{wrongIdentity}, []model.Event{accepted}) {
		t.Fatal("declared evidence scope with mismatched payload identity was accepted")
	}
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	conflict, err := DetectConflict(context.Background(), state, principal, PushBatch{BatchID: "BATCH-EVIDENCE", Events: []model.Event{wrongIdentity}}, []model.Event{accepted}, teamTestTime)
	if err != nil || conflict == nil || conflict.Type != ConflictEvidenceMismatch {
		t.Fatalf("normalized evidence DetectConflict() = %#v, %v", conflict, err)
	}
}

func TestEvidenceConflictDetectsSharedPayloadIDAcrossDifferentDeclaredScopes(t *testing.T) {
	accepted := evidenceConflictEventWithPayloadID(t, "EVT-EVIDENCE-TEAM", "evidence:EV-A", "EV-A", "sha-1", "base-1")
	candidate := evidenceConflictEventWithPayloadID(t, "EVT-EVIDENCE-LOCAL", "evidence:EV-B", "EV-A", "sha-1", "base-1")
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	conflict, err := DetectConflict(context.Background(), state, principal, PushBatch{BatchID: "BATCH-EVIDENCE", Events: []model.Event{candidate}}, []model.Event{accepted}, teamTestTime)
	if err != nil || conflict == nil || conflict.Type != ConflictEvidenceMismatch {
		t.Fatalf("cross-scope evidence DetectConflict() = %#v, %v", conflict, err)
	}
}

func TestEvidenceConflictRejectsMismatchedScopeAndPayloadWithoutPeer(t *testing.T) {
	candidate := evidenceConflictEventWithPayloadID(t, "EVT-EVIDENCE-LOCAL", "evidence:EV-A", "EV-B", "sha-1", "base-1")
	principal := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	state := testProjectState(t)
	conflict, err := DetectConflict(context.Background(), state, principal, PushBatch{BatchID: "BATCH-EVIDENCE", Events: []model.Event{candidate}}, nil, teamTestTime)
	if err != nil || conflict == nil || conflict.Type != ConflictEvidenceMismatch {
		t.Fatalf("self-mismatched evidence DetectConflict() = %#v, %v", conflict, err)
	}
}

func TestResolveConflict(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	lead := testPrincipal("USR-LEAD", model.ActorHuman, model.RoleLead)
	first := testGoalProposalEvent(t, lead, "BATCH-ACCEPTED", 1, "EVT-ACCEPTED", "GCH-ACCEPTED")
	first.Sync.AffectedScope = []string{"internal/team/conflict.go"}
	setPayloadDigest(&first)
	if result, err := service.Push(ctx, lead, PushBatch{BatchID: first.Sync.BatchID, BaseTeamSeq: 1, Events: []model.Event{first}}); err != nil || result.Status != PushAccepted {
		t.Fatalf("seed Push() = %#v, %v", result, err)
	}
	candidate := testGoalProposalEvent(t, lead, "BATCH-CONFLICT", 1, "EVT-CONFLICT", "GCH-CONFLICT")
	candidate.Sync.AffectedScope = []string{"internal/team/conflict.go"}
	setPayloadDigest(&candidate)
	opened, err := service.Push(ctx, lead, PushBatch{BatchID: candidate.Sync.BatchID, BaseTeamSeq: 1, Events: []model.Event{candidate}})
	if err != nil || opened.Status != PushConflict || opened.ConflictID == "" {
		t.Fatalf("conflicting Push() = %#v, %v", opened, err)
	}

	agent := testPrincipal("AGT-BUILD", model.ActorAgent, model.RoleAgent)
	if _, err := service.ResolveConflict(ctx, agent, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: AcceptTeam}); err == nil {
		t.Fatal("ResolveConflict() by agent succeeded, want authorization rejection")
	}

	resolved, err := service.ResolveConflict(ctx, lead, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: AcceptTeam})
	if err != nil || resolved.Status != PushAccepted || resolved.TeamSeqFrom != resolved.TeamSeqTo {
		t.Fatalf("ResolveConflict() = %#v, %v", resolved, err)
	}
	retry, err := service.ResolveConflict(ctx, lead, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: AcceptTeam})
	if err != nil || retry.TeamSeqFrom != resolved.TeamSeqFrom || retry.TeamSeqTo != resolved.TeamSeqTo {
		t.Fatalf("ResolveConflict() retry = %#v, %v; want stored result %#v", retry, err, resolved)
	}
}

func TestResolveConflictReplacementAndManualMergeReplay(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	opened := openScopeConflict(t, ctx, service, owner)
	nilSync := model.Event{ID: "EVT-NIL-SYNC"}
	result, err := service.ResolveConflict(ctx, owner, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: AcceptTeam, Replacement: []model.Event{nilSync}})
	if err != nil || result.Status != PushRejected {
		t.Fatalf("AcceptTeam with replacement = %#v, %v; want rejection", result, err)
	}

	opened = openScopeConflict(t, ctx, service, owner)
	mergeBase := uint64(len(readTeamAccepted(t, root)))
	proposal := testGoalProposalEvent(t, owner, "BATCH-MERGE", mergeBase, "EVT-MERGE-PROPOSAL", "GCH-MERGE")
	approval := testEventEnvelope(owner, "BATCH-MERGE", mergeBase, "EVT-MERGE-APPROVAL", "goal.change.approved")
	approval.AggregateType = "goal_change"
	approval.AggregateID = "GCH-MERGE"
	approval.Payload = mustJSON(t, model.GoalChangeApproved{GoalChangeID: "GCH-MERGE", DeciderID: owner.Actor.ID, DecidedAt: teamTestTime})
	setPayloadDigest(&approval)
	before := len(readTeamAccepted(t, root))
	result, err = service.ResolveConflict(ctx, owner, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: ManualMerge, Replacement: []model.Event{proposal, approval}})
	if err != nil || result.Status != PushRejected || len(readTeamAccepted(t, root)) != before {
		t.Fatalf("unconfirmed ManualMerge = %#v, %v; want rejection without append", result, err)
	}
	result, err = service.ResolveConflict(ctx, owner, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: ManualMerge, Replacement: []model.Event{proposal, approval}, Confirmed: true})
	if err != nil || result.Status != PushAccepted {
		t.Fatalf("ManualMerge = %#v, %v", result, err)
	}
	history := readTeamAccepted(t, root)
	state, err := model.Reduce(history)
	if err != nil || state.Goal.Version != 2 {
		t.Fatalf("manual merge replay = %#v, %v", state.Goal, err)
	}
	resolution := history[len(history)-1]
	if resolution.Type != "conflict.resolved" || resolution.GoalVersion != 2 || resolution.Sync.BaseTeamSeq != resolution.Sequence-1 {
		t.Fatalf("resolution after goal merge = %#v", resolution)
	}
}

func TestKeepAsProposalAppendsGoalProposal(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	opened := openScopeConflict(t, ctx, service, owner)
	result, err := service.ResolveConflict(ctx, owner, ConflictResolutionRequest{ConflictID: opened.ConflictID, Action: KeepAsProposal})
	if err != nil || result.Status != PushAccepted || len(result.Events) != 2 {
		t.Fatalf("KeepAsProposal = %#v, %v", result, err)
	}
	if result.Events[0].Type != "goal.change.proposed" || result.Events[1].Type != "conflict.resolved" {
		t.Fatalf("KeepAsProposal events = %#v", result.Events)
	}
}

func TestResolveConflictAuthorization(t *testing.T) {
	tests := []struct {
		name         string
		kind         model.ActorKind
		role         model.ActorRole
		conflictType string
		wantAllowed  bool
	}{
		{name: "agent denied", kind: model.ActorAgent, role: model.RoleAgent, conflictType: ConflictScopeOverlap},
		{name: "lead resolves scope", kind: model.ActorHuman, role: model.RoleLead, conflictType: ConflictScopeOverlap, wantAllowed: true},
		{name: "reviewer resolves evidence", kind: model.ActorHuman, role: model.RoleReviewer, conflictType: ConflictEvidenceMismatch, wantAllowed: true},
		{name: "lead cannot resolve stale goal", kind: model.ActorHuman, role: model.RoleLead, conflictType: ConflictStaleGoal},
		{name: "owner resolves stale goal", kind: model.ActorHuman, role: model.RoleOwner, conflictType: ConflictStaleGoal, wantAllowed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			principal := testPrincipal("USR-RESOLVER", test.kind, test.role)
			err := authorizeConflictResolution(principal, model.Conflict{Type: test.conflictType})
			if (err == nil) != test.wantAllowed {
				t.Fatalf("authorizeConflictResolution() error = %v, allowed = %t", err, test.wantAllowed)
			}
		})
	}
}

func acceptedScopeEvent(t *testing.T, scope string) model.Event {
	t.Helper()
	return model.Event{Sync: &model.SyncMetadata{AffectedScope: []string{scope}}}
}

func evidenceConflictEvent(t *testing.T, id, scope, sha, baseline string) model.Event {
	return evidenceConflictEventWithPayloadID(t, id, scope, "EVD-1", sha, baseline)
}

func evidenceConflictEventWithPayloadID(t *testing.T, id, scope, evidenceID, sha, baseline string) model.Event {
	t.Helper()
	return model.Event{ID: id, Type: "evidence.candidate.recorded", GoalVersion: 1, Payload: mustJSON(t, model.EvidenceCandidateRecorded{Evidence: model.Evidence{ID: evidenceID, SHA256: sha, Baseline: baseline}}), Sync: &model.SyncMetadata{AffectedScope: []string{scope}}}
}

func designConflictEvent(t *testing.T, id, scope, title string) model.Event {
	t.Helper()
	return model.Event{ID: id, Type: "requirement.planned", GoalVersion: 1, Payload: mustJSON(t, model.RequirementPlanned{Requirement: model.Requirement{ID: "REQ-API", GoalVersion: 1, Title: title, Constraints: []string{"stable"}}}), Sync: &model.SyncMetadata{AffectedScope: []string{scope}}}
}

func openScopeConflict(t *testing.T, ctx context.Context, service *Service, owner Principal) PushResult {
	t.Helper()
	base := uint64(len(readTeamAccepted(t, service.root)))
	accepted := testGoalProposalEvent(t, owner, "BATCH-SEED-"+fmt.Sprint(base), base, "EVT-SEED-"+fmt.Sprint(base), "GCH-SEED-"+fmt.Sprint(base))
	accepted.Sync.AffectedScope = []string{"internal/team/conflict.go"}
	setPayloadDigest(&accepted)
	if result, err := service.Push(ctx, owner, PushBatch{BatchID: accepted.Sync.BatchID, BaseTeamSeq: base, Events: []model.Event{accepted}}); err != nil || result.Status != PushAccepted {
		t.Fatalf("seed Push() = %#v, %v", result, err)
	}
	candidate := testGoalProposalEvent(t, owner, "BATCH-CONFLICT-"+fmt.Sprint(base), base, "EVT-CONFLICT-"+fmt.Sprint(base), "GCH-CONFLICT-"+fmt.Sprint(base))
	candidate.Sync.AffectedScope = []string{"internal/team/conflict.go"}
	setPayloadDigest(&candidate)
	opened, err := service.Push(ctx, owner, PushBatch{BatchID: candidate.Sync.BatchID, BaseTeamSeq: base, Events: []model.Event{candidate}})
	if err != nil || opened.Status != PushConflict {
		t.Fatalf("conflicting Push() = %#v, %v", opened, err)
	}
	return opened
}

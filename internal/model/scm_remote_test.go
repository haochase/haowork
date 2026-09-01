package model

import (
	"strings"
	"testing"
	"time"
)

const testRemoteID = "RSCM-001"

func TestRemoteSCMObservationsReplayWithoutChangingGovernanceState(t *testing.T) {
	events := initializedRemoteSCMEvents(t)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.SCMRemotes[testRemoteID].Provider; got != "github" {
		t.Fatalf("provider = %q, want github", got)
	}
	pull := state.SCMPullRequests[SCMRemotePullKey(testRemoteID, 7)]
	if pull.HeadOID != strings.Repeat("b", 40) || pull.MergeCommitOID != strings.Repeat("c", 40) {
		t.Fatalf("pull request projection = %#v", pull)
	}
	if state.Tasks["TSK-001"].Status != StatusApproved {
		t.Fatalf("remote observation changed task status to %q", state.Tasks["TSK-001"].Status)
	}
	if len(state.Evidence["TSK-001"]) != 0 {
		t.Fatal("remote check was promoted to Evidence")
	}
	if got := state.SCMRemoteRefs[SCMRemoteRefKey(testRemoteID, "refs/heads/main")].CommitOID; got != strings.Repeat("c", 40) {
		t.Fatalf("remote ref OID = %q", got)
	}
	if got := state.SCMReviews[SCMRemoteReviewKey(testRemoteID, 81)].State; got != "APPROVED" {
		t.Fatalf("remote review state = %q", got)
	}
}

func TestRemoteSCMRepeatedSnapshotIsIdempotent(t *testing.T) {
	events := initializedRemoteSCMEvents(t)
	repeated := events[len(events)-1]
	repeated.ID = "EVT-REMOTE-CHECK-REPEATED"
	repeated.OccurredAt = repeated.OccurredAt.Add(time.Minute)
	events = append(events, repeated)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SCMChecks) != 1 {
		t.Fatalf("check projection count = %d, want 1", len(state.SCMChecks))
	}
}

func TestRemoteSCMRejectsStaleAndCrossRemoteSnapshots(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		events := initializedRemoteSCMEvents(t)
		check := validRemoteCheck()
		check.ObservedAt = check.ObservedAt.Add(-time.Minute)
		stale := remoteModelEvent(t, "EVT-REMOTE-CHECK-STALE", "scm.remote.check.observed", "scm_remote_check", SCMRemoteCheckKey(testRemoteID, "check-1"), SCMRemoteCheckObserved{Observation: check})
		if _, err := Reduce(append(events, stale)); err == nil {
			t.Fatal("Reduce() accepted stale check snapshot")
		}
	})

	t.Run("cross remote", func(t *testing.T) {
		events := initializedRemoteSCMEvents(t)
		check := validRemoteCheck()
		check.RemoteID = "RSCM-UNKNOWN"
		crossRemote := remoteModelEvent(t, "EVT-REMOTE-CHECK-CROSS", "scm.remote.check.observed", "scm_remote_check", SCMRemoteCheckKey(check.RemoteID, check.ExternalID), SCMRemoteCheckObserved{Observation: check})
		if _, err := Reduce(append(events, crossRemote)); err == nil {
			t.Fatal("Reduce() accepted check for unknown remote")
		}
	})
}

func TestRemoteSCMRegistrationRequiresHumanOwner(t *testing.T) {
	events := validRemoteBaseEvents(t)
	registered := remoteModelEvent(t, "EVT-REMOTE", "scm.remote.registered", "scm_remote", testRemoteID, SCMRemoteRegistered{Remote: validRemote()})
	registered.Actor = Actor{ID: "USR-LEAD", Kind: ActorHuman, Role: RoleLead}

	if _, err := Reduce(append(events, registered)); err == nil {
		t.Fatal("Reduce() accepted remote registration by non-owner")
	}
}

func TestRemoteSCMCompletedCheckCannotRegress(t *testing.T) {
	events := initializedRemoteSCMEvents(t)
	regressed := validRemoteCheck()
	regressed.Status = "queued"
	regressed.Conclusion = ""
	regressed.CompletedAt = nil
	regressed.ObservedAt = regressed.ObservedAt.Add(time.Minute)
	event := remoteModelEvent(t, "EVT-REMOTE-CHECK-REGRESSED", "scm.remote.check.observed", "scm_remote_check", SCMRemoteCheckKey(testRemoteID, regressed.ExternalID), SCMRemoteCheckObserved{Observation: regressed})

	if _, err := Reduce(append(events, event)); err == nil {
		t.Fatal("Reduce() accepted a completed check regression")
	}
}

func TestRemoteSCMAllowsMergedPullWithoutMergeCommitOID(t *testing.T) {
	events := validRemoteBaseEvents(t)
	pull := validRemotePull()
	pull.MergeCommitOID = ""
	event := remoteModelEvent(t, "EVT-REMOTE-MERGED-NO-OID", "scm.remote.pull_request.observed", "scm_remote_pull", SCMRemotePullKey(testRemoteID, pull.Number), SCMRemotePullRequestObserved{Observation: pull})
	events = append(events,
		remoteModelEvent(t, "EVT-REMOTE", "scm.remote.registered", "scm_remote", testRemoteID, SCMRemoteRegistered{Remote: validRemote()}),
		event,
	)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.SCMPullRequests[SCMRemotePullKey(testRemoteID, pull.Number)].MergedAt == nil {
		t.Fatal("merged PR state was not preserved")
	}
}

func initializedRemoteSCMEvents(t *testing.T) []Event {
	t.Helper()
	events := validRemoteBaseEvents(t)
	events = append(events,
		remoteModelEvent(t, "EVT-REMOTE", "scm.remote.registered", "scm_remote", testRemoteID, SCMRemoteRegistered{Remote: validRemote()}),
		remoteModelEvent(t, "EVT-REMOTE-REF", "scm.remote.ref.observed", "scm_remote_ref", SCMRemoteRefKey(testRemoteID, "refs/heads/main"), SCMRemoteRefObserved{Observation: SCMRemoteRefObservation{
			RemoteID: testRemoteID, Ref: "refs/heads/main", CommitOID: strings.Repeat("c", 40), Change: "created", ObservedAt: goalTestTime.Add(2 * time.Minute),
		}}),
		remoteModelEvent(t, "EVT-REMOTE-PR", "scm.remote.pull_request.observed", "scm_remote_pull", SCMRemotePullKey(testRemoteID, 7), SCMRemotePullRequestObserved{Observation: validRemotePull()}),
		remoteModelEvent(t, "EVT-REMOTE-REVIEW", "scm.remote.review.observed", "scm_remote_review", SCMRemoteReviewKey(testRemoteID, 81), SCMRemoteReviewObserved{Observation: validRemoteReview()}),
		remoteModelEvent(t, "EVT-REMOTE-CHECK", "scm.remote.check.observed", "scm_remote_check", SCMRemoteCheckKey(testRemoteID, "check-1"), SCMRemoteCheckObserved{Observation: validRemoteCheck()}),
	)
	return events
}

func validRemoteBaseEvents(t *testing.T) []Event {
	t.Helper()
	events, _ := validMissionReplayEvents(t)
	events = append(events, scmProjectEvent(t, "PRJ-TEST", "EVT-SCM-REPO", "scm.repository.registered", "scm_repository", "SCM-001", SCMRepositoryRegistered{Repository: SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-TEST", Provider: "local-git", ObjectFormat: "sha1", RemoteFingerprint: testSCMDigest, RegisteredAt: goalTestTime,
	}}))
	return events
}

func validRemote() SCMRemote {
	return SCMRemote{
		ID: testRemoteID, RepositoryID: "SCM-001", Provider: "github", ProviderRepositoryFingerprint: testSCMDigest,
		APIHostSHA256: strings.Repeat("a", 64), RegisteredAt: goalTestTime.Add(time.Minute),
	}
}

func validRemotePull() SCMPullRequestObservation {
	mergedAt := goalTestTime.Add(3 * time.Minute)
	return SCMPullRequestObservation{
		RemoteID: testRemoteID, Number: 7, State: "closed", TitleSHA256: strings.Repeat("d", 64), AuthorSHA256: strings.Repeat("e", 64),
		BaseRef: "refs/heads/main", BaseOID: strings.Repeat("a", 40), HeadRef: "refs/heads/chase/change", HeadRepositorySHA256: strings.Repeat("f", 64),
		HeadOID: strings.Repeat("b", 40), CommitOIDs: []string{strings.Repeat("b", 40)}, MergeCommitOID: strings.Repeat("c", 40),
		MergedAt: &mergedAt, GitHubUpdatedAt: mergedAt, ObservedAt: mergedAt.Add(time.Minute),
	}
}

func validRemoteCheck() SCMCheckObservation {
	startedAt := goalTestTime.Add(4 * time.Minute)
	completedAt := startedAt.Add(time.Minute)
	return SCMCheckObservation{
		RemoteID: testRemoteID, ExternalID: "check-1", Source: "check-run", CommitOID: strings.Repeat("b", 40),
		Name: "test (ubuntu-latest)", Status: "completed", Conclusion: "success", StartedAt: &startedAt, CompletedAt: &completedAt,
		ObservedAt: completedAt.Add(time.Minute),
	}
}

func validRemoteReview() SCMReviewObservation {
	submittedAt := goalTestTime.Add(4 * time.Minute)
	return SCMReviewObservation{
		RemoteID: testRemoteID, PullNumber: 7, ReviewID: 81, CommitOID: strings.Repeat("b", 40), ReviewerSHA256: strings.Repeat("9", 64),
		State: "APPROVED", SubmittedAt: submittedAt, ObservedAt: submittedAt.Add(time.Minute),
	}
}

func remoteModelEvent(t *testing.T, id, eventType, aggregateType, aggregateID string, payload any) Event {
	t.Helper()
	event := scmProjectEvent(t, "PRJ-TEST", id, eventType, aggregateType, aggregateID, payload)
	event.Actor = Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}
	event.OccurredAt = goalTestTime.Add(10 * time.Minute)
	return event
}

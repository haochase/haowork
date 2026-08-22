package model

import (
	"testing"
	"time"
)

const testCommitOID = "0123456789012345678901234567890123456789"
const testSCMDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestReduceRejectsSCMCommitWithUnknownRepository(t *testing.T) {
	event := scmModelEvent(t, "EVT-SCM-001", "scm.commit.observed", "scm_commit", "SCM-UNKNOWN:"+testCommitOID, SCMCommitObserved{
		Observation: CommitObservation{RepositoryID: "SCM-UNKNOWN", CommitOID: testCommitOID, TreeOID: testCommitOID},
	})

	if _, err := Reduce([]Event{initializedEvent(t), event}); err == nil {
		t.Fatal("Reduce() accepted commit for unknown repository")
	}
}

func TestReduceProjectsSCMRepositoryCommitAndBindingLifecycle(t *testing.T) {
	events, mission := validMissionReplayEvents(t)
	projectID := events[0].ProjectID
	events = append(events,
		scmProjectEvent(t, projectID, "EVT-SCM-MISSION", "mission.issued", "mission", mission.ID, MissionIssued{Envelope: mission}),
		scmProjectEvent(t, projectID, "EVT-SCM-RUN-START", "run.started", "run", "RUN-SCM-001", RunStarted{Run: Run{
			ID: "RUN-SCM-001", TaskID: mission.GovernanceTaskIDs[0], GoalVersion: 1, Executor: "codex", ActorID: "AGT-BUILD",
		}}),
		scmProjectEvent(t, projectID, "EVT-SCM-RUN-FINISH", "run.finished", "run", "RUN-SCM-001", RunFinished{
			RunID: "RUN-SCM-001", Result: "implemented",
		}),
		scmProjectEvent(t, projectID, "EVT-SCM-EVIDENCE", "evidence.recorded", "task", mission.GovernanceTaskIDs[0], EvidenceRecorded{Evidence: Evidence{
			ID: "EVD-SCM-001", TaskID: mission.GovernanceTaskIDs[0], RunID: "RUN-SCM-001", Kind: "test", URI: "artifacts/scm.log", SHA256: testCommitOID, Outcome: "pass",
		}}),
		scmProjectEvent(t, projectID, "EVT-SCM-REPO", "scm.repository.registered", "scm_repository", "SCM-001", SCMRepositoryRegistered{Repository: SCMRepository{
			ID: "SCM-001", ProjectID: projectID, Provider: "local-git", ObjectFormat: "sha1", RemoteFingerprint: testSCMDigest, RegisteredAt: goalTestTime,
		}}),
		scmProjectEvent(t, projectID, "EVT-SCM-COMMIT", "scm.commit.observed", "scm_commit", "SCM-001:"+testCommitOID, SCMCommitObserved{Observation: CommitObservation{
			RepositoryID: "SCM-001", CommitOID: testCommitOID, TreeOID: testCommitOID, AuthorName: "Developer", AuthorEmailSHA256: testSCMDigest,
			CommitterName: "Developer", CommitterEmailSHA256: testSCMDigest, AuthoredAt: goalTestTime, CommittedAt: goalTestTime,
			Message: "implement governed change", Changes: []SCMFileChange{{Path: "internal/model/model.go", Status: "modified", OldBlobOID: testCommitOID, NewBlobOID: testCommitOID}},
		}}),
	)
	binding := SCMBinding{
		ID: "SCB-001", RepositoryID: "SCM-001", CommitOID: testCommitOID, ProjectID: projectID, GoalVersion: 1,
		TaskIDs: mission.GovernanceTaskIDs, MissionID: mission.ID, EvidenceIDs: []string{"EVD-SCM-001"}, ScopedChanges: []string{"internal/model/model.go"},
		Status: "proposed", PolicyVersion: "scm-v1",
	}
	events = append(events,
		scmProjectEvent(t, projectID, "EVT-SCM-PROPOSE", "scm.binding.proposed", "scm_binding", binding.ID, SCMBindingProposed{Binding: binding}),
		scmProjectEvent(t, projectID, "EVT-SCM-CONFIRM", "scm.binding.confirmed", "scm_binding", binding.ID, SCMBindingConfirmed{
			BindingID: binding.ID, ConfirmedBy: "USR-001", ConfirmedAt: goalTestTime.Add(time.Minute),
		}),
	)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.SCMRepositories["SCM-001"].Provider != "local-git" {
		t.Fatalf("repository = %#v", state.SCMRepositories["SCM-001"])
	}
	if state.CommitObservations[SCMCommitKey("SCM-001", testCommitOID)].Message != "implement governed change" {
		t.Fatalf("commit = %#v", state.CommitObservations)
	}
	if state.SCMBindings[binding.ID].Status != "confirmed" {
		t.Fatalf("binding = %#v", state.SCMBindings[binding.ID])
	}
	if state.Tasks[mission.GovernanceTaskIDs[0]].Status == StatusCompleted {
		t.Fatal("SCM binding completed the governance task")
	}
}

func scmProjectEvent(t *testing.T, projectID, id, eventType, aggregateType, aggregateID string, payload any) Event {
	t.Helper()
	event := scmModelEvent(t, id, eventType, aggregateType, aggregateID, payload)
	event.ProjectID = projectID
	return event
}

func scmModelEvent(t *testing.T, id, eventType, aggregateType, aggregateID string, payload any) Event {
	t.Helper()
	event := testEvent(t, id, eventType, payload)
	event.AggregateType = aggregateType
	event.AggregateID = aggregateID
	return event
}

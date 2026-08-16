package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testProjectID   = "PRJ-001"
	testGoalVersion = 1
)

var goalTestTime = time.Date(2026, 8, 9, 9, 30, 0, 0, time.UTC)

func TestReduceAppliesTaskEvidenceStateMachine(t *testing.T) {
	events := validWorkflowEvents(t)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.ProjectID != testProjectID {
		t.Fatalf("ProjectID = %q, want %q", state.ProjectID, testProjectID)
	}
	if state.Goal.Version != testGoalVersion || state.Goal.Statement != "Keep work auditable" {
		t.Fatalf("Goal = %#v, want version 1 with the initialized statement", state.Goal)
	}
	if got := state.Requirements["REQ-001"].Status; got != StatusApproved {
		t.Fatalf("requirement status = %q, want %q", got, StatusApproved)
	}
	if got := state.Tasks["TSK-001"].Status; got != StatusCompleted {
		t.Fatalf("task status = %q, want %q", got, StatusCompleted)
	}
	if got := state.Tasks["TSK-001"].LastRunID; got != "RUN-001" {
		t.Fatalf("task LastRunID = %q, want RUN-001", got)
	}
	run := state.Runs["RUN-001"]
	if run.Status != StatusFinished || run.Result != "implemented" {
		t.Fatalf("run = %#v, want Finished with result", run)
	}
	evidence := state.Evidence["TSK-001"]
	if len(evidence) != 1 || evidence[0].ID != "EVD-001" {
		t.Fatalf("evidence = %#v, want EVD-001", evidence)
	}
}

func TestReduceTransitionsHaveExactIntermediateStates(t *testing.T) {
	tests := []struct {
		name        string
		eventCount  int
		requirement Status
		task        Status
		run         Status
	}{
		{name: "project initialized", eventCount: 1},
		{name: "requirement planned", eventCount: 2, requirement: StatusDraft, task: StatusDraft},
		{name: "requirement approved", eventCount: 3, requirement: StatusApproved, task: StatusApproved},
		{name: "run started", eventCount: 4, requirement: StatusApproved, task: StatusRunning, run: StatusRunning},
		{name: "run finished", eventCount: 5, requirement: StatusApproved, task: StatusVerifying, run: StatusFinished},
		{name: "passing evidence recorded", eventCount: 6, requirement: StatusApproved, task: StatusVerified, run: StatusFinished},
		{name: "task completed", eventCount: 7, requirement: StatusApproved, task: StatusCompleted, run: StatusFinished},
	}
	events := validWorkflowEvents(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := Reduce(events[:test.eventCount])
			if err != nil {
				t.Fatal(err)
			}
			if state.Requirements["REQ-001"].Status != test.requirement {
				t.Fatalf("requirement status = %q, want %q", state.Requirements["REQ-001"].Status, test.requirement)
			}
			if state.Tasks["TSK-001"].Status != test.task {
				t.Fatalf("task status = %q, want %q", state.Tasks["TSK-001"].Status, test.task)
			}
			if state.Runs["RUN-001"].Status != test.run {
				t.Fatalf("run status = %q, want %q", state.Runs["RUN-001"].Status, test.run)
			}
		})
	}
}

func TestReduceApprovesEveryTaskInRequirement(t *testing.T) {
	events := validWorkflowEvents(t)
	var planned RequirementPlanned
	if err := json.Unmarshal(events[1].Payload, &planned); err != nil {
		t.Fatal(err)
	}
	planned.Tasks = append(planned.Tasks, Task{
		ID:                 "TSK-002",
		RequirementID:      "REQ-001",
		GoalVersion:        1,
		Title:              "Review reducer",
		AcceptanceCriteria: []string{"review is clean"},
	})
	events[1].Payload = marshalPayload(t, planned)

	state, err := Reduce(events[:3])
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"TSK-001", "TSK-002"} {
		if got := state.Tasks[taskID].Status; got != StatusApproved {
			t.Fatalf("task %s status = %q, want %q", taskID, got, StatusApproved)
		}
	}
}

func TestReduceRejectsIllegalTransitions(t *testing.T) {
	tests := []struct {
		name    string
		events  func(*testing.T) []Event
		wantErr string
	}{
		{
			name: "start unapproved task",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				return append(events[:2], events[3])
			},
			wantErr: `task "TSK-001" status "Draft", requires "Approved"`,
		},
		{
			name: "complete without evidence",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				return append(events[:5], events[6])
			},
			wantErr: `task "TSK-001" status "Verifying", requires "Verified"`,
		},
		{
			name: "approve unknown requirement",
			events: func(t *testing.T) []Event {
				return []Event{
					initializedEvent(t),
					testEvent(t, "EVT-002", "requirement.approved", RequirementApproved{RequirementID: "REQ-MISSING"}),
				}
			},
			wantErr: `requirement "REQ-MISSING" not found`,
		},
		{
			name: "finish unknown run",
			events: func(t *testing.T) []Event {
				return []Event{
					initializedEvent(t),
					testEvent(t, "EVT-002", "run.finished", RunFinished{RunID: "RUN-MISSING", Result: "none"}),
				}
			},
			wantErr: `run "RUN-MISSING" not found`,
		},
		{
			name: "record evidence for unknown task",
			events: func(t *testing.T) []Event {
				return []Event{
					initializedEvent(t),
					testEvent(t, "EVT-002", "evidence.recorded", EvidenceRecorded{Evidence: Evidence{ID: "EVD-001", TaskID: "TSK-MISSING", Outcome: "pass"}}),
				}
			},
			wantErr: `task "TSK-MISSING" not found`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Reduce(test.events(t))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReduceRequiresInitializedGoal(t *testing.T) {
	tests := []struct {
		name             string
		goal             GoalVersion
		eventGoalVersion int
		wantErr          string
	}{
		{
			name:             "empty statement",
			goal:             GoalVersion{Version: 1, CompletionCriteria: []string{"task is verified"}},
			eventGoalVersion: 1,
			wantErr:          "goal statement is required",
		},
		{
			name:             "missing completion criteria",
			goal:             GoalVersion{Version: 1, Statement: "Keep work auditable"},
			eventGoalVersion: 1,
			wantErr:          "at least one completion criterion is required",
		},
		{
			name:             "blank completion criterion",
			goal:             GoalVersion{Version: 1, Statement: "Keep work auditable", CompletionCriteria: []string{"  "}},
			eventGoalVersion: 1,
			wantErr:          "at least one completion criterion is required",
		},
		{
			name:             "version other than one",
			goal:             GoalVersion{Version: 2, Statement: "Keep work auditable", CompletionCriteria: []string{"task is verified"}},
			eventGoalVersion: 2,
			wantErr:          "goal version must be 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := testEvent(t, "EVT-001", "project.initialized", ProjectInitialized{Name: "demo", Goal: test.goal})
			event.GoalVersion = test.eventGoalVersion
			_, err := Reduce([]Event{event})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReduceRejectsSecondProjectInitialization(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string
	}{
		{
			name:    "matching envelope",
			mutate:  func(*Event) {},
			wantErr: "project is already initialized",
		},
		{
			name: "mismatched project before malformed payload",
			mutate: func(event *Event) {
				event.ProjectID = "PRJ-STALE"
				event.Payload = json.RawMessage(`{"invalid"`)
			},
			wantErr: `project id "PRJ-STALE" does not match "PRJ-001"`,
		},
		{
			name: "mismatched goal before malformed payload",
			mutate: func(event *Event) {
				event.GoalVersion = 2
				event.Payload = json.RawMessage(`{"invalid"`)
			},
			wantErr: "goal version 2 does not match 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			second := initializedEvent(t)
			second.ID = "EVT-002"
			test.mutate(&second)
			_, err := Reduce([]Event{initializedEvent(t), second})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "payload") || strings.Contains(err.Error(), "JSON") {
				t.Fatalf("Reduce() decoded payload before envelope validation: %v", err)
			}
		})
	}
}

func TestReduceRejectsMismatchedEnvelopeBeforePayload(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr string
	}{
		{
			name: "project ID",
			mutate: func(event *Event) {
				event.ProjectID = "PRJ-STALE"
			},
			wantErr: `project id "PRJ-STALE" does not match "PRJ-001"`,
		},
		{
			name: "goal version",
			mutate: func(event *Event) {
				event.GoalVersion = 2
			},
			wantErr: "goal version 2 does not match 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			later := Event{
				ID:          "EVT-002",
				Type:        "requirement.planned",
				ProjectID:   testProjectID,
				GoalVersion: testGoalVersion,
				Payload:     json.RawMessage(`{"invalid"`),
			}
			test.mutate(&later)
			_, err := Reduce([]Event{initializedEvent(t), later})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
			if strings.Contains(err.Error(), "payload") || strings.Contains(err.Error(), "JSON") {
				t.Fatalf("Reduce() decoded payload before envelope validation: %v", err)
			}
		})
	}
}

func TestReduceRejectsDuplicateIDs(t *testing.T) {
	tests := []struct {
		name    string
		events  func(*testing.T) []Event
		wantErr string
	}{
		{
			name: "event",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				events[1].ID = events[0].ID
				return events[:2]
			},
			wantErr: `duplicate event id "EVT-001"`,
		},
		{
			name: "requirement",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				duplicate := RequirementPlanned{Requirement: Requirement{ID: "REQ-001", GoalVersion: 1, Title: "duplicate"}}
				return append(events[:2], testEvent(t, "EVT-NEW", "requirement.planned", duplicate))
			},
			wantErr: `requirement id "REQ-001" already exists`,
		},
		{
			name: "task within one plan",
			events: func(t *testing.T) []Event {
				planned := RequirementPlanned{
					Requirement: Requirement{ID: "REQ-001", GoalVersion: 1, Title: "requirement"},
					Tasks: []Task{
						{ID: "TSK-001", RequirementID: "REQ-001", GoalVersion: 1, Title: "first"},
						{ID: "TSK-001", RequirementID: "REQ-001", GoalVersion: 1, Title: "second"},
					},
				}
				return []Event{initializedEvent(t), testEvent(t, "EVT-002", "requirement.planned", planned)}
			},
			wantErr: `task id "TSK-001" already exists`,
		},
		{
			name: "run",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				second := testEvent(t, "EVT-NEW", "run.started", RunStarted{Run: Run{ID: "RUN-001", TaskID: "TSK-001", GoalVersion: 1}})
				return append(events[:5], second)
			},
			wantErr: `run id "RUN-001" already exists`,
		},
		{
			name: "evidence",
			events: func(t *testing.T) []Event {
				events := validWorkflowEvents(t)
				first := events[5]
				first.Payload = marshalPayload(t, EvidenceRecorded{Evidence: Evidence{ID: "EVD-001", TaskID: "TSK-001", Outcome: "fail"}})
				second := testEvent(t, "EVT-NEW", "evidence.recorded", EvidenceRecorded{Evidence: Evidence{ID: "EVD-001", TaskID: "TSK-001", Outcome: "pass"}})
				return append(append(events[:5], first), second)
			},
			wantErr: `evidence id "EVD-001" already exists`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Reduce(test.events(t))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReduceRejectsUnknownEventType(t *testing.T) {
	unknown := testEvent(t, "EVT-002", "task.magically_completed", map[string]string{"task_id": "TSK-001"})
	_, err := Reduce([]Event{initializedEvent(t), unknown})
	if err == nil || !strings.Contains(err.Error(), `unknown event type "task.magically_completed"`) {
		t.Fatalf("Reduce() error = %v, want unknown event type", err)
	}
}

func TestReduceRecordsNonPassingEvidenceWithoutVerifyingTask(t *testing.T) {
	events := validWorkflowEvents(t)
	events[5].Payload = marshalPayload(t, EvidenceRecorded{Evidence: Evidence{
		ID:      "EVD-001",
		TaskID:  "TSK-001",
		Kind:    "test",
		URI:     "artifacts/test.log",
		SHA256:  "abc123",
		Outcome: "fail",
	}})

	state, err := Reduce(events[:6])
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tasks["TSK-001"].Status; got != StatusVerifying {
		t.Fatalf("task status = %q, want %q", got, StatusVerifying)
	}
	if len(state.Evidence["TSK-001"]) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(state.Evidence["TSK-001"]))
	}
}

func TestReduceInvalidatesChangeAttributionWhenContentChanges(t *testing.T) {
	events := []Event{
		initializedEvent(t),
		testEvent(t, "EVT-002", "requirement.planned", RequirementPlanned{
			Requirement: Requirement{ID: "REQ-001", GoalVersion: testGoalVersion, Title: "Govern changes"},
			Tasks: []Task{{
				ID: "TSK-001", RequirementID: "REQ-001", GoalVersion: testGoalVersion,
				Title: "Implement scanner", AcceptanceCriteria: []string{"reject stale attribution"},
			}},
		}),
		testEvent(t, "EVT-003", "changes.scanned", ChangesScanned{Changes: []FileChange{{
			Path: "api.go", Status: "modified", SHA256: "before", Baseline: "baseline",
		}}}),
		testEvent(t, "EVT-004", "change.attributed", ChangeAttributed{
			Path: "api.go", SHA256: "before", TaskID: "TSK-001", Note: "owned by implementation task",
		}),
		testEvent(t, "EVT-005", "changes.scanned", ChangesScanned{Changes: []FileChange{{
			Path: "api.go", Status: "modified", SHA256: "after", Baseline: "baseline",
		}}}),
	}

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	change, exists := state.Changes["api.go"]
	if !exists {
		t.Fatal("api.go change missing from projection")
	}
	if change.SHA256 != "after" {
		t.Fatalf("SHA256 = %q, want current content hash", change.SHA256)
	}
	if change.Attributed {
		t.Fatal("changed content retained stale attribution")
	}
}

func TestReduceRejectsInvalidChangeAttributionReplay(t *testing.T) {
	tests := []struct {
		name        string
		attribution ChangeAttributed
		want        string
	}{
		{
			name: "unknown task",
			attribution: ChangeAttributed{
				Path: "api.go", SHA256: "changed", TaskID: "TSK-MISSING", Note: "not a known task",
			},
			want: `task "TSK-MISSING" not found`,
		},
		{
			name: "external manual without note",
			attribution: ChangeAttributed{
				Path: "api.go", SHA256: "changed", TaskID: "external-manual", Note: " \t ",
			},
			want: "external-manual attribution requires a note",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []Event{
				initializedEvent(t),
				testEvent(t, "EVT-002", "changes.scanned", ChangesScanned{Changes: []FileChange{{
					Path: "api.go", Status: "modified", SHA256: "changed", Baseline: "baseline",
				}}}),
				testEvent(t, "EVT-003", "change.attributed", test.attribution),
			}

			_, err := Reduce(events)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Reduce() error = %v, want replay rejection containing %q", err, test.want)
			}
		})
	}
}

func TestReduceDoesNotMakeActorPermissionDecisions(t *testing.T) {
	events := validWorkflowEvents(t)
	events[2].Actor = Actor{ID: "AGT-001", Kind: ActorAgent, Role: RoleAgent}
	events[6].Actor = Actor{ID: "AGT-001", Kind: ActorAgent, Role: RoleAgent}

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Tasks["TSK-001"].Status; got != StatusCompleted {
		t.Fatalf("task status = %q, want %q", got, StatusCompleted)
	}
}

func TestReduceContextRunCheckpointEvidenceEvents(t *testing.T) {
	state, err := Reduce(testP003Events(t))
	if err != nil {
		t.Fatal(err)
	}
	if state.Contexts["ctx-1"].SliceHash != "slice-hash" {
		t.Fatal("context not replayed")
	}
	if state.Runs["run-1"].ContextID != "ctx-1" {
		t.Fatal("run context not replayed")
	}
	if state.Checkpoints["checkpoint-1"].AdapterCursor != "cursor-1" {
		t.Fatal("checkpoint not replayed")
	}
	if len(state.ExecutorEvents) != 1 {
		t.Fatalf("executor event count = %d", len(state.ExecutorEvents))
	}
	if state.Evidence["task-1"][0].Status != "verified" {
		t.Fatal("verified evidence not replayed")
	}
}

func TestReduceRejectsDuplicateContextSupersede(t *testing.T) {
	events := testP003Events(t)
	events = append(events,
		testEvent(t, "EVT-012", "context.superseded", ContextSuperseded{ContextID: "ctx-1"}),
		testEvent(t, "EVT-013", "context.superseded", ContextSuperseded{ContextID: "ctx-1"}),
	)
	if _, err := Reduce(events); err == nil || !strings.Contains(err.Error(), `context "ctx-1" is already superseded`) {
		t.Fatalf("Reduce() duplicate supersede error = %v, want already superseded", err)
	}
}

func TestReduceDuplicateExecutorCursorIsIdempotent(t *testing.T) {
	state, err := Reduce(testP003EventsWithDuplicateCursor(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ExecutorEvents) != 1 {
		t.Fatalf("duplicate cursor created state: %d", len(state.ExecutorEvents))
	}
}

func TestReduceRejectsSecondStepFinishWithoutOverwritingProjectedStep(t *testing.T) {
	events := testP003Events(t)[:6]
	firstFinish := testEvent(t, "EVT-FIRST-FINISH", "run.step.finished", RunStepFinished{Step: Step{
		ID: "step-1", RunID: "run-1", Summary: "first result", ArtifactRefs: []ArtifactRef{{Kind: "log", URI: "first.log", SHA256: "first"}},
	}})
	projected, err := Reduce(append(events, firstFinish))
	if err != nil {
		t.Fatal(err)
	}
	if step := projected.Steps["step-1"]; step.Status != "finished" || step.Summary != "first result" || step.ArtifactRefs[0].URI != "first.log" {
		t.Fatalf("first finished step = %+v, want original finished projection", step)
	}

	secondFinish := testEvent(t, "EVT-SECOND-FINISH", "run.step.finished", RunStepFinished{Step: Step{
		ID: "step-1", RunID: "run-1", Summary: "overwritten result", ArtifactRefs: []ArtifactRef{{Kind: "log", URI: "second.log", SHA256: "second"}},
	}})
	_, err = Reduce(append(append(events, firstFinish), secondFinish))
	if err == nil || !strings.Contains(err.Error(), `step "step-1" status "finished", requires "started"`) {
		t.Fatalf("Reduce() error = %v, want duplicate step finish rejection", err)
	}
	if step := projected.Steps["step-1"]; step.Summary != "first result" || step.ArtifactRefs[0].URI != "first.log" {
		t.Fatalf("previous projection was overwritten: %+v", step)
	}
}

func TestReducePausedFailedAndCancelledRunsCannotCompleteTask(t *testing.T) {
	for kind, wantStatus := range map[string]Status{
		"paused":    StatusPaused,
		"failed":    StatusFailed,
		"cancelled": StatusCancelled,
	} {
		t.Run(kind, func(t *testing.T) {
			events := validWorkflowEvents(t)[:4]
			events = append(events,
				testEvent(t, "EVT-PAUSE", "executor.event.received", ExecutorEventReceived{ExecutorEvent: ExecutorEvent{RunID: "RUN-001", Kind: kind, Cursor: "cursor-1"}}),
				testEvent(t, "EVT-COMPLETE", "task.completed", TaskCompleted{TaskID: "TSK-001"}),
			)
			_, err := Reduce(events)
			if err == nil || !strings.Contains(err.Error(), `task "TSK-001" status "Running", requires "Verified"`) {
				t.Fatalf("Reduce() error = %v, want direct completion blocked for %s run", err, kind)
			}
			state, err := Reduce(events[:5])
			if err != nil {
				t.Fatal(err)
			}
			if got := state.Runs["RUN-001"].Status; got != wantStatus {
				t.Fatalf("run status = %q, want %q", got, wantStatus)
			}
		})
	}
}

func TestReduceKeepsP002EvidenceRecordedCompatibility(t *testing.T) {
	state, err := Reduce(testLegacyEventsWithPassingEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks["task-1"].Status != StatusVerified {
		t.Fatal("legacy pass no longer verifies task")
	}
}

func TestReduceGoalApprovalAdvancesVersionAndStalesBindings(t *testing.T) {
	events := testP003Events(t)
	lease := Lease{
		ID:                 "lease-1",
		TaskID:             "task-1",
		SubjectKind:        "task",
		SubjectID:          "task-1",
		EnvironmentID:      "env-1",
		AgentTeamsInstance: "teams-1",
		ContextID:          "ctx-1",
		GoalVersion:        1,
		Revision:           1,
		AllowedScopes:      []string{"internal/model"},
		AllowedSkills:      []string{"go"},
		RiskLevel:          "low",
		Status:             "active",
		StartsAt:           goalTestTime,
		ExpiresAt:          goalTestTime.Add(time.Hour),
	}
	change := GoalChange{
		ID:          "goal-change-1",
		Reason:      "Add durable team coordination",
		Status:      "proposed",
		ProposerID:  "USR-001",
		BaseVersion: 1,
		Proposed: GoalVersion{
			Version:            2,
			Statement:          "Coordinate team work without silent goal drift",
			Invariants:         []string{"human approval advances the goal"},
			CompletionCriteria: []string{"all active bindings use the approved goal"},
		},
		CreatedAt: goalTestTime,
	}
	events = append(events,
		testEvent(t, "EVT-LEASE-001", "lease.issued", LeaseIssued{Lease: lease}),
		testEvent(t, "EVT-GOAL-001", "goal.change.proposed", GoalChangeProposed{GoalChange: change}),
		testEvent(t, "EVT-GOAL-002", "goal.change.approved", GoalChangeApproved{
			GoalChangeID: change.ID,
			DeciderID:    "USR-OWNER",
			DecidedAt:    goalTestTime.Add(time.Minute),
		}),
	)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Goal.Version; got != 2 {
		t.Fatalf("goal version = %d, want 2", got)
	}
	if got := state.GoalChanges[change.ID].Status; got != "approved" {
		t.Fatalf("goal change status = %q, want approved", got)
	}
	if got := state.Contexts["ctx-1"].Superseded; !got {
		t.Fatal("current context was not superseded after goal approval")
	}
	if got := state.Evidence["task-1"][0].Status; got != "stale" {
		t.Fatalf("evidence status = %q, want stale", got)
	}
	if got := state.Leases[lease.ID].Status; got != "stale" {
		t.Fatalf("lease status = %q, want stale", got)
	}
}

func TestReduceLeaseLifecycleKeepsRevisionAndTerminalHistory(t *testing.T) {
	base := Lease{
		ID:                 "lease-release",
		TaskID:             "task-1",
		SubjectKind:        "task",
		SubjectID:          "task-1",
		EnvironmentID:      "env-1",
		AgentTeamsInstance: "teams-1",
		ContextID:          "ctx-1",
		GoalVersion:        1,
		Revision:           1,
		AllowedScopes:      []string{"internal/model"},
		AllowedSkills:      []string{"go"},
		RiskLevel:          "low",
		Status:             "active",
		StartsAt:           goalTestTime,
		ExpiresAt:          goalTestTime.Add(time.Hour),
	}
	revoked := base
	revoked.ID = "lease-revoke"
	events := append(testP003Events(t)[:3],
		testEvent(t, "EVT-LEASE-001", "lease.issued", LeaseIssued{Lease: base}),
		testEvent(t, "EVT-LEASE-002", "lease.renewed", LeaseRenewed{LeaseID: base.ID, ExpiresAt: goalTestTime.Add(2 * time.Hour)}),
		testEvent(t, "EVT-LEASE-003", "lease.released", LeaseReleased{LeaseID: base.ID}),
		testEvent(t, "EVT-LEASE-004", "lease.issued", LeaseIssued{Lease: revoked}),
		testEvent(t, "EVT-LEASE-005", "lease.revoked", LeaseRevoked{LeaseID: revoked.ID}),
	)

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if lease := state.Leases[base.ID]; lease.Revision != 3 || lease.Status != "released" {
		t.Fatalf("released lease = %#v, want revision 3 and terminal history", lease)
	}
	if lease := state.Leases[revoked.ID]; lease.Revision != 2 || lease.Status != "revoked" {
		t.Fatalf("revoked lease = %#v, want revision 2 and terminal history", lease)
	}

	_, err = Reduce(append(events, testEvent(t, "EVT-LEASE-006", "lease.renewed", LeaseRenewed{
		LeaseID:   base.ID,
		ExpiresAt: goalTestTime.Add(3 * time.Hour),
	})))
	if err == nil || !strings.Contains(err.Error(), `lease "lease-release" is terminal`) {
		t.Fatalf("terminal renewal error = %v, want terminal lease rejection", err)
	}
}

func TestReduceConflictResolutionAppendsWithoutRewritingLocalBranch(t *testing.T) {
	localEvents := []Event{
		testEvent(t, "EVT-LOCAL-001", "requirement.planned", RequirementPlanned{Requirement: Requirement{
			ID: "req-local", GoalVersion: 1, Title: "Keep local branch evidence",
		}}),
	}
	before, err := json.Marshal(localEvents)
	if err != nil {
		t.Fatal(err)
	}
	conflict := Conflict{
		ID:               "conflict-1",
		Type:             "concurrent-edit",
		EntityID:         "task-1",
		Status:           "open",
		CommonBase:       8,
		TeamVersion:      10,
		LocalVersion:     9,
		AffectedScope:    []string{"internal/model"},
		SuggestedActions: []string{"review local branch"},
		LocalEvents:      localEvents,
		CreatedAt:        goalTestTime,
	}
	events := []Event{
		initializedEvent(t),
		testEvent(t, "EVT-CONFLICT-001", "conflict.opened", ConflictOpened{Conflict: conflict}),
		testEvent(t, "EVT-CONFLICT-002", "conflict.resolved", ConflictResolved{
			ConflictID: conflict.ID,
			ResolverID: "USR-OWNER",
			Resolution: "kept local branch for review",
			ResolvedAt: goalTestTime.Add(time.Minute),
		}),
	}
	events[2].Actor = Actor{ID: "USR-OWNER", Kind: ActorHuman, Role: RoleOwner}

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	resolved := state.Conflicts[conflict.ID]
	if resolved.Status != "resolved" || resolved.ResolverID != "USR-OWNER" || resolved.Resolution != "kept local branch for review" {
		t.Fatalf("resolved conflict = %#v, want recorded resolver and action", resolved)
	}
	after, err := json.Marshal(resolved.LocalEvents)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("local branch was rewritten: got %s, want %s", after, before)
	}
}

func TestReduceConflictResolutionRejectsUnauthorizedResolver(t *testing.T) {
	conflict := Conflict{ID: "conflict-authority", Type: "concurrent-edit", EntityID: "task-1"}
	baseEvents := []Event{
		initializedEvent(t),
		testEvent(t, "EVT-CONFLICT-AUTH-001", "conflict.opened", ConflictOpened{Conflict: conflict}),
	}

	tests := []struct {
		name     string
		actor    Actor
		resolver string
		wantErr  string
	}{
		{
			name:     "agent actor cannot resolve conflict",
			actor:    Actor{ID: "AGT-001", Kind: ActorAgent, Role: RoleAgent},
			resolver: "AGT-001",
			wantErr:  "conflict resolver must be a human owner, lead, or reviewer",
		},
		{
			name:     "payload cannot impersonate another resolver",
			actor:    Actor{ID: "USR-REVIEWER", Kind: ActorHuman, Role: RoleReviewer},
			resolver: "USR-OWNER",
			wantErr:  "conflict resolver id \"USR-OWNER\" does not match event actor \"USR-REVIEWER\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := testEvent(t, "EVT-CONFLICT-AUTH-002", "conflict.resolved", ConflictResolved{
				ConflictID: conflict.ID,
				ResolverID: tt.resolver,
				Resolution: "recorded resolution",
				ResolvedAt: goalTestTime.Add(time.Minute),
			})
			resolved.Actor = tt.actor

			_, err := Reduce(append(baseEvents, resolved))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Reduce() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestReduceKeepsP003HistoryWithoutSyncMetadata(t *testing.T) {
	events := testP003Events(t)
	before, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}

	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Evidence["task-1"][0].Status; got != "verified" {
		t.Fatalf("P0-03 evidence status = %q, want verified", got)
	}
	for _, event := range events {
		if event.Sync != nil {
			t.Fatalf("legacy event %q unexpectedly has sync metadata", event.ID)
		}
	}
	after, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("legacy P0-03 event JSON changed after replay: got %s, want %s", after, before)
	}
}

func TestReduceCompletesTaskWithVerifiedContextualEvidence(t *testing.T) {
	events := append(testP003Events(t), testEvent(t, "EVT-012", "task.completed", TaskCompleted{TaskID: "task-1"}))
	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks["task-1"].Status != StatusCompleted {
		t.Fatalf("task status = %q, want %q", state.Tasks["task-1"].Status, StatusCompleted)
	}
	if state.Evidence["task-1"][0].Actor.ID != "AGT-producer" {
		t.Fatalf("evidence producer = %q, want AGT-producer", state.Evidence["task-1"][0].Actor.ID)
	}
}

func TestReduceInvalidatedContextualEvidenceBlocksCompletion(t *testing.T) {
	verified, err := Reduce(testP003Events(t))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Tasks["task-1"].Status != StatusVerified {
		t.Fatalf("verified task status = %q, want %q", verified.Tasks["task-1"].Status, StatusVerified)
	}

	events := append(testP003Events(t),
		testEvent(t, "EVT-012", "evidence.invalidated", EvidenceInvalidated{Evidence: Evidence{ID: "evidence-1"}}),
		testEvent(t, "EVT-013", "task.completed", TaskCompleted{TaskID: "task-1"}),
	)
	_, err = Reduce(events)
	if err == nil || !strings.Contains(err.Error(), `task "task-1" status "Verifying", requires "Verified"`) {
		t.Fatalf("Reduce() error = %v, want invalidated evidence to block completion", err)
	}
}

func TestReduceRejectsInvalidContextualRunBinding(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, []Event) []Event
		wantErr string
	}{
		{
			name: "context without hash",
			mutate: func(t *testing.T, events []Event) []Event {
				events[4].Payload = marshalPayload(t, RunStarted{Run: Run{ID: "run-1", TaskID: "task-1", GoalVersion: testGoalVersion, ContextID: "ctx-1"}})
				return events[:5]
			},
			wantErr: "context id and context hash must be provided together",
		},
		{
			name: "context belongs to another task",
			mutate: func(t *testing.T, events []Event) []Event {
				events[3].Payload = marshalPayload(t, ContextIssued{Context: ContextSlice{ID: "ctx-1", TaskID: "other-task", GoalVersion: testGoalVersion, SliceHash: "slice-hash"}})
				var planned RequirementPlanned
				if err := json.Unmarshal(events[1].Payload, &planned); err != nil {
					t.Fatal(err)
				}
				planned.Tasks = append(planned.Tasks, Task{ID: "other-task", RequirementID: "req-1", GoalVersion: testGoalVersion, Title: "Other task"})
				events[1].Payload = marshalPayload(t, planned)
				return events[:5]
			},
			wantErr: `context "ctx-1" belongs to task "other-task", not "task-1"`,
		},
		{
			name: "mismatched hash",
			mutate: func(t *testing.T, events []Event) []Event {
				events[4].Payload = marshalPayload(t, RunStarted{Run: Run{ID: "run-1", TaskID: "task-1", GoalVersion: testGoalVersion, ContextID: "ctx-1", ContextHash: "wrong-hash"}})
				return events[:5]
			},
			wantErr: `run "run-1" context hash does not match context "ctx-1"`,
		},
		{
			name: "superseded context",
			mutate: func(t *testing.T, events []Event) []Event {
				superseded := testEvent(t, "EVT-004A", "context.superseded", ContextSuperseded{ContextID: "ctx-1"})
				return append(append(append([]Event{}, events[:4]...), superseded), events[4])
			},
			wantErr: `context "ctx-1" is superseded`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Reduce(test.mutate(t, testP003Events(t)))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReduceRejectsContextualEvidenceBeforeFinishedRun(t *testing.T) {
	events := testP003Events(t)
	withoutFinish := append(events[:8], events[9])
	_, err := Reduce(withoutFinish)
	if err == nil || !strings.Contains(err.Error(), `run "run-1" status "Running", requires "Finished"`) {
		t.Fatalf("Reduce() error = %v, want candidate evidence to require a finished run", err)
	}
}

func TestReduceRejectsContextualEvidenceBoundToDifferentContext(t *testing.T) {
	events := testP003Events(t)
	var planned RequirementPlanned
	if err := json.Unmarshal(events[1].Payload, &planned); err != nil {
		t.Fatal(err)
	}
	planned.Tasks = append(planned.Tasks, Task{ID: "other-task", RequirementID: "req-1", GoalVersion: testGoalVersion, Title: "Other task"})
	events[1].Payload = marshalPayload(t, planned)
	context := testEvent(t, "EVT-004A", "context.issued", ContextIssued{Context: ContextSlice{ID: "ctx-2", TaskID: "other-task", GoalVersion: testGoalVersion, SliceHash: "other-hash"}})
	events = append(events[:4], append([]Event{context}, events[4:]...)...)
	for index := range events {
		if events[index].Type != "evidence.candidate.recorded" {
			continue
		}
		events[index].Payload = marshalPayload(t, EvidenceCandidateRecorded{Evidence: Evidence{
			ID: "evidence-1", TaskID: "task-1", RunID: "run-1", ContextID: "ctx-2", GoalVersion: testGoalVersion,
			Kind: "test", URI: "artifacts/test.log", SHA256: "evidence-hash",
		}})
	}
	_, err := Reduce(events[:11])
	if err == nil || !strings.Contains(err.Error(), `context "ctx-2" belongs to task "other-task", not "task-1"`) {
		t.Fatalf("Reduce() error = %v, want evidence context task binding rejection", err)
	}
}

func TestReduceRejectsCrossRunStepReferencesAndCheckpointHash(t *testing.T) {
	base := testP003EventsWithSecondRun(t)
	tests := []struct {
		name    string
		event   Event
		wantErr string
	}{
		{
			name:    "checkpoint cross run step",
			event:   testEvent(t, "EVT-020", "checkpoint.created", CheckpointCreated{Checkpoint: Checkpoint{ID: "checkpoint-2", RunID: "run-2", StepID: "step-1", ContextHash: "slice-hash-2"}}),
			wantErr: `step "step-1" belongs to run "run-1", not "run-2"`,
		},
		{
			name:    "checkpoint mismatched context hash",
			event:   testEvent(t, "EVT-021", "checkpoint.created", CheckpointCreated{Checkpoint: Checkpoint{ID: "checkpoint-2", RunID: "run-1", StepID: "step-1", ContextHash: "wrong-hash"}}),
			wantErr: `checkpoint "checkpoint-2" context hash does not match run "run-1"`,
		},
		{
			name:    "executor event cross run step",
			event:   testEvent(t, "EVT-022", "executor.event.received", ExecutorEventReceived{ExecutorEvent: ExecutorEvent{RunID: "run-2", StepID: "step-1", Cursor: "cursor-2"}}),
			wantErr: `step "step-1" belongs to run "run-1", not "run-2"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Reduce(append(base, test.event))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Reduce() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func validWorkflowEvents(t *testing.T) []Event {
	t.Helper()
	return []Event{
		initializedEvent(t),
		testEvent(t, "EVT-002", "requirement.planned", RequirementPlanned{
			Requirement: Requirement{ID: "REQ-001", GoalVersion: 1, Title: "Audit workflow"},
			Tasks: []Task{{
				ID:                 "TSK-001",
				RequirementID:      "REQ-001",
				GoalVersion:        1,
				Title:              "Implement reducer",
				AcceptanceCriteria: []string{"state transitions are tested"},
			}},
		}),
		testEvent(t, "EVT-003", "requirement.approved", RequirementApproved{RequirementID: "REQ-001"}),
		testEvent(t, "EVT-004", "run.started", RunStarted{Run: Run{
			ID:          "RUN-001",
			TaskID:      "TSK-001",
			GoalVersion: 1,
			Executor:    "codex",
			ActorID:     "AGT-001",
		}}),
		testEvent(t, "EVT-005", "run.finished", RunFinished{RunID: "RUN-001", Result: "implemented"}),
		testEvent(t, "EVT-006", "evidence.recorded", EvidenceRecorded{Evidence: Evidence{
			ID:      "EVD-001",
			TaskID:  "TSK-001",
			Kind:    "test",
			URI:     "artifacts/test.log",
			SHA256:  "abc123",
			Outcome: "pass",
		}}),
		testEvent(t, "EVT-007", "task.completed", TaskCompleted{TaskID: "TSK-001"}),
	}
}

func testP003Events(t *testing.T) []Event {
	t.Helper()
	producer := Actor{ID: "AGT-producer", Kind: ActorAgent, Role: RoleAgent}
	candidate := testEvent(t, "EVT-010", "evidence.candidate.recorded", EvidenceCandidateRecorded{Evidence: Evidence{
		ID: "evidence-1", TaskID: "task-1", RunID: "run-1", ContextID: "ctx-1", GoalVersion: testGoalVersion,
		Kind: "test", URI: "artifacts/test.log", SHA256: "evidence-hash",
	}})
	candidate.Actor = producer
	return []Event{
		initializedEvent(t),
		testEvent(t, "EVT-002", "requirement.planned", RequirementPlanned{
			Requirement: Requirement{ID: "req-1", GoalVersion: testGoalVersion, Title: "Record execution context"},
			Tasks: []Task{{
				ID: "task-1", RequirementID: "req-1", GoalVersion: testGoalVersion,
				Title: "Replay an execution", AcceptanceCriteria: []string{"context is projected"},
			}},
		}),
		testEvent(t, "EVT-003", "requirement.approved", RequirementApproved{RequirementID: "req-1"}),
		testEvent(t, "EVT-004", "context.issued", ContextIssued{Context: ContextSlice{
			ID: "ctx-1", TaskID: "task-1", GoalVersion: testGoalVersion,
			Summary: "Use the approved execution context", SliceHash: "slice-hash",
		}}),
		testEvent(t, "EVT-005", "run.started", RunStarted{Run: Run{
			ID: "run-1", TaskID: "task-1", GoalVersion: testGoalVersion, Executor: "adapter", ActorID: "AGT-001",
			ContextID: "ctx-1", ContextHash: "slice-hash",
		}}),
		testEvent(t, "EVT-006", "run.step.started", RunStepStarted{Step: Step{ID: "step-1", RunID: "run-1", Kind: "command", Summary: "run checks"}}),
		testEvent(t, "EVT-007", "checkpoint.created", CheckpointCreated{Checkpoint: Checkpoint{
			ID: "checkpoint-1", RunID: "run-1", StepID: "step-1", ContextHash: "slice-hash", WorkspaceDigest: "workspace-hash", AdapterCursor: "cursor-1",
		}}),
		testEvent(t, "EVT-008", "executor.event.received", ExecutorEventReceived{ExecutorEvent: ExecutorEvent{
			RunID: "run-1", StepID: "step-1", Kind: "stdout", Cursor: "cursor-1", Summary: "checks passed",
		}}),
		testEvent(t, "EVT-009", "run.finished", RunFinished{RunID: "run-1", Result: "implemented"}),
		candidate,
		testEvent(t, "EVT-011", "evidence.verified", EvidenceVerified{Evidence: Evidence{
			ID: "evidence-1", TaskID: "task-1", RunID: "run-1", ContextID: "ctx-1", GoalVersion: testGoalVersion,
			Kind: "test", URI: "artifacts/test.log", SHA256: "evidence-hash",
		}}),
	}
}

func testP003EventsWithDuplicateCursor(t *testing.T) []Event {
	t.Helper()
	events := testP003Events(t)
	duplicate := testEvent(t, "EVT-012", "executor.event.received", ExecutorEventReceived{ExecutorEvent: ExecutorEvent{
		RunID: "run-1", StepID: "step-1", Kind: "stdout", Cursor: "cursor-1", Summary: "duplicate delivery",
	}})
	return append(events, duplicate)
}

func testP003EventsWithSecondRun(t *testing.T) []Event {
	t.Helper()
	events := testP003Events(t)
	var planned RequirementPlanned
	if err := json.Unmarshal(events[1].Payload, &planned); err != nil {
		t.Fatal(err)
	}
	planned.Tasks = append(planned.Tasks, Task{ID: "task-2", RequirementID: "req-1", GoalVersion: testGoalVersion, Title: "Second task"})
	events[1].Payload = marshalPayload(t, planned)
	context := testEvent(t, "EVT-004A", "context.issued", ContextIssued{Context: ContextSlice{ID: "ctx-2", TaskID: "task-2", GoalVersion: testGoalVersion, SliceHash: "slice-hash-2"}})
	run := testEvent(t, "EVT-005A", "run.started", RunStarted{Run: Run{ID: "run-2", TaskID: "task-2", GoalVersion: testGoalVersion, ContextID: "ctx-2", ContextHash: "slice-hash-2"}})
	return append(events[:4], append([]Event{context, run}, events[4:8]...)...)
}

func testLegacyEventsWithPassingEvidence(t *testing.T) []Event {
	t.Helper()
	events := testP003Events(t)
	return []Event{events[0], events[1], events[2],
		testEvent(t, "EVT-LEGACY-000", "run.started", RunStarted{Run: Run{
			ID: "run-1", TaskID: "task-1", GoalVersion: testGoalVersion, Executor: "adapter", ActorID: "AGT-001",
		}}),
		testEvent(t, "EVT-LEGACY-001", "run.finished", RunFinished{RunID: "run-1", Result: "implemented"}),
		testEvent(t, "EVT-LEGACY-002", "evidence.recorded", EvidenceRecorded{Evidence: Evidence{
			ID: "legacy-evidence-1", TaskID: "task-1", Kind: "test", URI: "artifacts/legacy.log", SHA256: "legacy-hash", Outcome: "pass",
		}}),
	}
}

func initializedEvent(t *testing.T) Event {
	t.Helper()
	return testEvent(t, "EVT-001", "project.initialized", ProjectInitialized{
		Name: "demo",
		Goal: GoalVersion{
			Version:            testGoalVersion,
			Statement:          "Keep work auditable",
			Invariants:         []string{"no silent goal drift"},
			CompletionCriteria: []string{"verified task can complete"},
		},
	})
}

func testEvent(t *testing.T, id, eventType string, payload any) Event {
	t.Helper()
	return Event{
		ID:          id,
		Type:        eventType,
		ProjectID:   testProjectID,
		GoalVersion: testGoalVersion,
		Actor:       Actor{ID: "USR-001", Kind: ActorHuman, Role: RoleOwner},
		Payload:     marshalPayload(t, payload),
	}
}

func marshalPayload(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const teamAITestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestTeamAISourceSnapshot(t *testing.T) {
	ready := TeamAIBinding{ID: "TAI-001", ProjectID: testProjectID, TeamRepo: "team/repo", TeamRevision: "rev-1", Status: "Ready", AssetDigest: teamAITestDigest}
	for _, tt := range []struct {
		name    string
		mutate  func(*TeamAIBinding)
		wantErr bool
	}{
		{"valid Ready", func(*TeamAIBinding) {}, false},
		{"wrong project", func(b *TeamAIBinding) { b.ProjectID = "other" }, true},
		{"Bound", func(b *TeamAIBinding) { b.Status = "Bound" }, true},
		{"missing digest", func(b *TeamAIBinding) { b.AssetDigest = "" }, true},
		{"uppercase digest", func(b *TeamAIBinding) { b.AssetDigest = strings.ToUpper(teamAITestDigest) }, true},
		{"short digest", func(b *TeamAIBinding) { b.AssetDigest = "abcd" }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			binding := ready
			tt.mutate(&binding)
			state := ProjectState{ProjectID: testProjectID, TeamAIBindings: map[string]TeamAIBinding{binding.ID: binding}}
			_, err := BuildTeamAISourceSnapshot(state, binding.ID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("build error = %v, wantErr %v", err, tt.wantErr)
			}
			snapshot := TeamAISourceSnapshot{BindingID: ready.ID, TeamRepo: ready.TeamRepo, TeamRevision: ready.TeamRevision, AssetDigest: ready.AssetDigest}
			if err := ValidateTeamAISourceSnapshot(state, &snapshot); (err != nil) != tt.wantErr {
				t.Fatalf("validate error = %v", err)
			}
		})
	}
	state := ProjectState{ProjectID: testProjectID, TeamAIBindings: map[string]TeamAIBinding{ready.ID: ready}}
	if _, err := BuildTeamAISourceSnapshot(state, "missing"); err == nil {
		t.Fatal("accepted missing binding")
	}
	if err := ValidateTeamAISourceSnapshot(state, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildTeamAISourceSnapshot(state, " TAI-001 ")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["binding_id"] != ready.ID || fields["team_repo"] != ready.TeamRepo || fields["team_revision"] != ready.TeamRevision || fields["asset_digest"] != ready.AssetDigest {
		t.Fatalf("snapshot JSON = %s", encoded)
	}
	for _, digest := range []string{"", "abcd", strings.ToUpper(teamAITestDigest), strings.Repeat("g", 64)} {
		changed := snapshot
		changed.AssetDigest = digest
		if err := ValidateTeamAISourceSnapshot(state, &changed); err == nil {
			t.Fatalf("accepted digest %q", digest)
		}
	}
	for _, field := range []string{"binding", "repo", "revision", "digest"} {
		t.Run("snapshot mismatch/"+field, func(t *testing.T) {
			changed := snapshot
			switch field {
			case "binding":
				changed.BindingID = "missing"
			case "repo":
				changed.TeamRepo = "other"
			case "revision":
				changed.TeamRevision = "other"
			case "digest":
				changed.AssetDigest = strings.Repeat("a", 64)
			}
			if err := ValidateTeamAISourceSnapshot(state, &changed); err == nil {
				t.Fatal("accepted snapshot mismatch")
			}
		})
	}
}

func TestTeamAIInitializesEmptyBindings(t *testing.T) {
	state, err := Reduce([]Event{initializedEvent(t)})
	if err != nil {
		t.Fatal(err)
	}
	bindings := teamAIProjectedBindings(t, state)
	if bindings == nil || len(bindings) != 0 {
		t.Fatalf("initialized bindings = %#v, want non-nil empty map", bindings)
	}
}

func TestTeamAIBindingRejectsWhitespaceIdentity(t *testing.T) {
	for _, id := range []string{" TAI-001", "TAI-001 ", "\tTAI-001\n"} {
		t.Run(id, func(t *testing.T) {
			bound := teamAITestBoundEvent(t)
			bound.AggregateID = id
			teamAIMutateBinding(t, &bound, "id", id)
			if _, err := Reduce([]Event{initializedEvent(t), bound}); err == nil {
				t.Fatal("accepted noncanonical binding identity")
			}
		})
	}
}

func TestTeamAIBindingLifecycle(t *testing.T) {
	bound := teamAITestBoundEvent(t)
	state, err := Reduce([]Event{initializedEvent(t), bound})
	if err != nil {
		t.Fatal(err)
	}
	binding := teamAIProjectedBindings(t, state)[bound.AggregateID]
	if binding.Status != "Bound" || binding.AssetDigest != "" || !binding.VerifiedAt.IsZero() {
		t.Fatalf("unverified binding = %#v", binding)
	}
	verified := teamAITestVerifiedEvent(t)
	state, err = Reduce([]Event{initializedEvent(t), bound, verified})
	if err != nil {
		t.Fatal(err)
	}
	binding = teamAIProjectedBindings(t, state)[bound.AggregateID]
	if binding.Status != "Ready" || binding.AssetDigest != teamAITestDigest || !binding.VerifiedAt.Equal(goalTestTime.Add(time.Minute)) {
		t.Fatalf("verified binding = %#v", binding)
	}
	if len(state.Requirements) != 0 || len(state.Evidence) != 0 || len(state.Approvals) != 0 {
		t.Fatal("TeamAI binding or asset verification promoted governance records")
	}
}

func TestTeamAIBindingAcceptsHumanLead(t *testing.T) {
	bound := teamAITestBoundEvent(t)
	bound.Actor.Role = RoleLead
	if _, err := Reduce([]Event{initializedEvent(t), bound}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamAIBindingRejectsUnauthorizedActors(t *testing.T) {
	for _, actor := range []Actor{
		{ID: "USR-001", Kind: ActorAgent, Role: RoleAgent},
		{ID: "USR-001", Kind: ActorAgent, Role: RoleOwner},
		{ID: "USR-001", Kind: ActorHuman, Role: RoleContributor},
		{ID: "USR-001", Kind: ActorHuman, Role: RoleReviewer},
	} {
		t.Run(string(actor.Kind)+"/"+string(actor.Role), func(t *testing.T) {
			bound := teamAITestBoundEvent(t)
			bound.Actor = actor
			if _, err := Reduce([]Event{initializedEvent(t), bound}); err == nil {
				t.Fatal("accepted unauthorized TeamAI binding")
			}
		})
	}
}

func TestTeamAIBindingReplayIsIdempotent(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			bound := teamAITestBoundEvent(t)
			events := []Event{initializedEvent(t), bound}
			if ready {
				events = append(events, teamAITestVerifiedEvent(t))
			}
			before, err := Reduce(events)
			if err != nil {
				t.Fatal(err)
			}
			duplicate := bound
			duplicate.ID += "-REPLAY"
			after, err := Reduce(append(events, duplicate))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("identical binding replay changed projected state")
			}
		})
	}
}

func TestTeamAIBindingRejectsConflictingPayload(t *testing.T) {
	for field, value := range map[string]any{
		"team_revision":   strings.Repeat("b", 40),
		"team_repo":       "https://example.com/other/team.git",
		"scope":           "user",
		"bound_at":        goalTestTime.Add(time.Second),
		"required_assets": []map[string]any{{"path": "rules/other.md", "kind": "rule", "sha256": teamAITestDigest}},
	} {
		t.Run(field, func(t *testing.T) {
			bound := teamAITestBoundEvent(t)
			conflict := bound
			conflict.ID += "-CONFLICT"
			teamAIMutateBinding(t, &conflict, field, value)
			if _, err := Reduce([]Event{initializedEvent(t), bound, conflict}); err == nil || !strings.Contains(err.Error(), "diverged") {
				t.Fatalf("conflicting binding error = %v, want divergence", err)
			}
		})
	}
}

func TestTeamAIBindingRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{"event project", func(e *Event) { e.ProjectID = "PRJ-OTHER" }},
		{"event goal", func(e *Event) { e.GoalVersion++ }},
		{"aggregate type", func(e *Event) { e.AggregateType = "project" }},
		{"aggregate id", func(e *Event) { e.AggregateID = "TAI-OTHER" }},
		{"binding project", func(e *Event) { teamAIMutateBinding(t, e, "project_id", "PRJ-OTHER") }},
		{"bound by", func(e *Event) { teamAIMutateBinding(t, e, "bound_by", "USR-OTHER") }},
		{"empty actor", func(e *Event) { e.Actor.ID = ""; teamAIMutateBinding(t, e, "bound_by", "") }},
		{"empty id", func(e *Event) { e.AggregateID = ""; teamAIMutateBinding(t, e, "id", "") }},
		{"empty repo", func(e *Event) { teamAIMutateBinding(t, e, "team_repo", "") }},
		{"empty revision", func(e *Event) { teamAIMutateBinding(t, e, "team_revision", "") }},
		{"invalid scope", func(e *Event) { teamAIMutateBinding(t, e, "scope", "global") }},
		{"empty assets", func(e *Event) { teamAIMutateBinding(t, e, "required_assets", []any{}) }},
		{"zero bound at", func(e *Event) { teamAIMutateBinding(t, e, "bound_at", time.Time{}) }},
		{"ready injection", func(e *Event) { teamAIMutateBinding(t, e, "status", "Ready") }},
		{"digest injection", func(e *Event) { teamAIMutateBinding(t, e, "asset_digest", teamAITestDigest) }},
		{"verified at injection", func(e *Event) { teamAIMutateBinding(t, e, "verified_at", goalTestTime) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bound := teamAITestBoundEvent(t)
			tt.mutate(&bound)
			if _, err := Reduce([]Event{initializedEvent(t), bound}); err == nil {
				t.Fatal("accepted invalid TeamAI binding")
			}
		})
	}
	if _, err := Reduce([]Event{teamAITestBoundEvent(t)}); err == nil {
		t.Fatal("accepted TeamAI binding before project initialization")
	}
}

func TestTeamAIAssetsVerificationRejectsUnknownBinding(t *testing.T) {
	if _, err := Reduce([]Event{initializedEvent(t), teamAITestVerifiedEvent(t)}); err == nil {
		t.Fatal("accepted verification for unknown binding")
	}
}

func TestTeamAIAssetsVerificationRejectsInvalidPayload(t *testing.T) {
	for _, digest := range []string{"", "sha256", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		t.Run("digest/"+digest, func(t *testing.T) {
			verified := teamAITestVerifiedEvent(t)
			var payload map[string]any
			if err := json.Unmarshal(verified.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload["asset_digest"] = digest
			verified.Payload = marshalPayload(t, payload)
			if _, err := Reduce([]Event{initializedEvent(t), teamAITestBoundEvent(t), verified}); err == nil {
				t.Fatal("accepted invalid asset digest")
			}
		})
	}
	for _, field := range []string{"verified_at", "aggregate_type", "aggregate_id", "project_id", "goal_version"} {
		t.Run(field, func(t *testing.T) {
			verified := teamAITestVerifiedEvent(t)
			switch field {
			case "verified_at":
				verified.Payload = marshalPayload(t, map[string]any{"binding_id": "TAI-001", "asset_digest": teamAITestDigest, "verified_at": time.Time{}})
			case "aggregate_type":
				verified.AggregateType = "project"
			case "aggregate_id":
				verified.AggregateID = "TAI-OTHER"
			case "project_id":
				verified.ProjectID = "PRJ-OTHER"
			case "goal_version":
				verified.GoalVersion++
			}
			if _, err := Reduce([]Event{initializedEvent(t), teamAITestBoundEvent(t), verified}); err == nil {
				t.Fatal("accepted invalid verification event")
			}
		})
	}
}

func teamAITestBoundEvent(t *testing.T) Event {
	t.Helper()
	event := testEvent(t, "EVT-TAI-BOUND", "teamai.bound", map[string]any{"binding": map[string]any{
		"id": "TAI-001", "project_id": testProjectID,
		"team_repo": "https://example.com/team/teamai.git", "team_revision": strings.Repeat("a", 40), "scope": "project",
		"required_assets": []map[string]any{{"path": "skills/haowork-governed-coding/SKILL.md", "kind": "skill", "sha256": teamAITestDigest}},
		"bound_by":        "USR-001", "bound_at": goalTestTime,
	}})
	event.AggregateType, event.AggregateID = "teamai_binding", "TAI-001"
	return event
}

func teamAITestVerifiedEvent(t *testing.T) Event {
	t.Helper()
	event := testEvent(t, "EVT-TAI-VERIFIED", "teamai.assets.verified", map[string]any{
		"binding_id": "TAI-001", "asset_digest": teamAITestDigest, "verified_at": goalTestTime.Add(time.Minute),
	})
	event.AggregateType, event.AggregateID = "teamai_binding", "TAI-001"
	return event
}

func teamAIMutateBinding(t *testing.T, event *Event, field string, value any) {
	t.Helper()
	var payload struct {
		Binding map[string]any `json:"binding"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Binding[field] = value
	event.Payload = marshalPayload(t, payload)
}

type teamAIProjectedBinding struct {
	Status      string    `json:"status"`
	AssetDigest string    `json:"asset_digest"`
	VerifiedAt  time.Time `json:"verified_at"`
}

func teamAIProjectedBindings(t *testing.T, state ProjectState) map[string]teamAIProjectedBinding {
	t.Helper()
	var projection struct {
		Bindings map[string]teamAIProjectedBinding `json:"teamai_bindings"`
	}
	if err := json.Unmarshal(marshalPayload(t, state), &projection); err != nil {
		t.Fatal(err)
	}
	return projection.Bindings
}

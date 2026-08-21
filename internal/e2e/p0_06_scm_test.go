package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestP006SCMCommitProvenanceAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := t.TempDir()
	now := time.Date(2026, time.August, 21, 1, 0, 0, 0, time.UTC)
	clock := testkit.Clock{Value: now}
	ids := &testkit.IDs{}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	reviewer := model.Actor{ID: "USR-REVIEWER", Kind: model.ActorHuman, Role: model.RoleReviewer}
	buildAgent := model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}

	if _, err := app.InitializeProject(ctx, app.InitializeProjectInput{
		Root: root, Name: "SCM provenance acceptance", ProjectID: "PRJ-SCM-E2E",
		Goal: "bind governed work to immutable commits", CompletionCriteria: []string{"binding survives replay"}, Actor: owner,
	}, ids, clock); err != nil {
		t.Fatal(err)
	}
	runP006Git(t, root, "init", "-b", "main")
	runP006Git(t, root, "config", "user.name", "SCM Acceptance")
	runP006Git(t, root, "config", "user.email", "scm-acceptance@example.test")
	writeP006File(t, root, "internal/app/base.txt", "baseline\n")
	runP006Git(t, root, "add", ".")
	runP006Git(t, root, "commit", "-m", "establish baseline")
	baselineOID := runP006Git(t, root, "rev-parse", "HEAD")

	project, err := core.Open(ctx, root, core.Dependencies{IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	requirement, tasks, err := project.Service.Plan(ctx, app.PlanInput{
		Title: "Governed SCM change", Tasks: []app.TaskInput{{Title: "Implement scoped change", AcceptanceCriteria: []string{"tests pass"}}}, Actor: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Service.Approve(ctx, requirement.ID, owner); err != nil {
		t.Fatal(err)
	}

	events := &p006EventAppender{project: project, now: now}
	contextSlice := model.ContextSlice{ID: "CTX-SCM-E2E", TaskID: tasks[0].ID, GoalVersion: 1, Revision: 1, SliceHash: strings.Repeat("c", 64), AllowedPaths: []string{"internal/app"}}
	events.append(t, "context.issued", "context", contextSlice.ID, owner, model.ContextIssued{Context: contextSlice})
	assignments := map[model.AgentFunction]string{model.FunctionBuild: "AGT-BUILD", model.FunctionVerify: "AGT-VERIFY"}
	for function, agentID := range assignments {
		events.append(t, "agent.identity.registered", "agent", agentID, owner, model.AgentIdentityRegistered{Agent: model.LogicalAgent{
			ID: agentID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: function,
		}})
	}
	lease := model.Lease{
		ID: "LSE-SCM-E2E", TaskID: tasks[0].ID, SubjectKind: "agent", SubjectID: "AGT-BUILD", EnvironmentID: "local",
		AgentTeamsInstance: "AT-SCM-E2E", ContextID: contextSlice.ID, GoalVersion: 1, Revision: 1,
		AllowedScopes: []string{"internal/app"}, AllowedSkills: []string{"scm-provenance"}, RiskLevel: "L2",
		StartsAt: now, ExpiresAt: now.Add(time.Hour),
	}
	events.append(t, "lease.issued", "lease", lease.ID, owner, model.LeaseIssued{Lease: lease})
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{
		ID: "MSN-SCM-E2E", ProjectID: "PRJ-SCM-E2E", ContextID: contextSlice.ID, ContextHash: contextSlice.SliceHash,
		LeaseID: lease.ID, PolicyVersion: "scm-v1", GoalVersion: 1, GovernanceTaskIDs: []string{tasks[0].ID},
		CompletionCriteria: []string{"tests pass"}, AllowedScopes: []string{"internal/app"},
		AllowedSkills: []model.MissionSkillGrant{{Name: "scm-provenance", Version: "1"}}, RoleAssignments: assignments,
		RiskLevel: "L2", EnvironmentID: "local", IssuedAt: now, Deadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	missionApproval, err := project.Service.RequestApproval(ctx, "mission", mission.ID, mission.Hash, mission.RiskLevel, buildAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.Service.DecideApproval(ctx, missionApproval.ID, mission.Hash, "approved", "reviewed mission", reviewer); err != nil {
		t.Fatal(err)
	}
	events.append(t, "mission.issued", "mission", mission.ID, buildAgent, model.MissionIssued{Envelope: mission})
	run, err := project.Service.StartRunWithContext(ctx, tasks[0].ID, "agentteams", contextSlice.ID, buildAgent)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Service.FinishRun(ctx, run.ID, "implemented", buildAgent); err != nil {
		t.Fatal(err)
	}
	evidence := model.Evidence{ID: "EVD-SCM-E2E", TaskID: tasks[0].ID, RunID: run.ID, ContextID: contextSlice.ID, GoalVersion: 1, Kind: "test", URI: "artifacts/scm-e2e.json", SHA256: strings.Repeat("e", 64), Outcome: "pass", Status: "verified", Actor: reviewer}
	events.append(t, "evidence.recorded", "evidence", evidence.ID, reviewer, model.EvidenceRecorded{Evidence: evidence})

	writeP006File(t, root, "internal/app/feature.txt", "governed change\n")
	runP006Git(t, root, "add", "internal/app/feature.txt")
	runP006Git(t, root, "commit", "-m", "implement governed SCM change")
	commitOID := runP006Git(t, root, "rev-parse", "HEAD")
	runP006Git(t, root, "branch", "accepted", commitOID)

	server, client := newP006LocalAPI(t, project)
	repository, err := client.RegisterSCM(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := client.ObserveSCMCommit(ctx, repository.ID, commitOID, buildAgent)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Changes) != 1 || observation.Changes[0].Path != "internal/app/feature.txt" {
		t.Fatalf("commit observation = %#v", observation)
	}
	binding, err := client.ProposeSCMBinding(ctx, app.ProposeSCMBindingInput{
		RepositoryID: repository.ID, CommitOID: commitOID, TaskIDs: []string{tasks[0].ID}, MissionID: mission.ID,
		EvidenceIDs: []string{evidence.ID}, TraceIDs: []string{"TRC-SCM-E2E"}, Actor: buildAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ConfirmSCMBinding(ctx, binding.ID, reviewer); err == nil {
		t.Fatal("L2 SCM binding confirmation succeeded without a hash-bound approval")
	} else {
		var apiErr *localapi.HTTPError
		if !errors.As(err, &apiErr) || apiErr.StatusCode < 400 {
			t.Fatalf("unapproved confirmation error = %v", err)
		}
	}
	bindingHash, err := model.SCMBindingPayloadSHA256(binding)
	if err != nil {
		t.Fatal(err)
	}
	bindingApproval, err := project.Service.RequestApproval(ctx, "scm_binding", binding.ID, bindingHash, "L2", buildAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.Service.DecideApproval(ctx, bindingApproval.ID, bindingHash, "approved", "reviewed commit binding", reviewer); err != nil {
		t.Fatal(err)
	}
	confirmed, err := client.ConfirmSCMBinding(ctx, binding.ID, reviewer)
	if err != nil || confirmed.Status != "confirmed" {
		t.Fatalf("confirmed binding = %#v, err = %v", confirmed, err)
	}
	beforeRestart, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()

	replayed, err := core.Open(ctx, root, core.Dependencies{IDs: ids, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	replayedServer, replayedClient := newP006LocalAPI(t, replayed)
	defer replayedServer.Close()
	afterRestart, err := replayedClient.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeRestart, afterRestart) {
		t.Fatalf("SCM projection changed after Core restart\nbefore=%#v\nafter=%#v", beforeRestart, afterRestart)
	}

	runP006Git(t, root, "branch", "-f", "accepted", baselineOID)
	report, err := replayedClient.VerifySCMHistory(ctx, repository.ID, []string{"refs/heads/accepted"}, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if report.Superseded != 1 || report.Invalidated != 1 {
		t.Fatalf("history report = %#v", report)
	}
	status, err := replayedClient.SCMStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Commits) != 1 || status.Commits[0].Status != "superseded" || len(status.Bindings) != 1 || status.Bindings[0].Status != "invalidated" {
		t.Fatalf("invalidated SCM status = %#v", status)
	}
	second, err := replayedClient.VerifySCMHistory(ctx, repository.ID, []string{"refs/heads/accepted"}, reviewer)
	if err != nil || second.Superseded != 0 || second.Invalidated != 0 {
		t.Fatalf("idempotent history report = %#v, err = %v", second, err)
	}
}

type p006EventAppender struct {
	project core.Project
	now     time.Time
	next    int
}

func (writer *p006EventAppender) append(t *testing.T, eventType, aggregateType, aggregateID string, actor model.Actor, payload any) {
	t.Helper()
	writer.next++
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.project.Events.Append(context.Background(), model.Event{
		ID:   fmt.Sprintf("EVT-P006-%03d", writer.next),
		Type: eventType, ProjectID: "PRJ-SCM-E2E", GoalVersion: 1, AggregateType: aggregateType, AggregateID: aggregateID,
		Actor: actor, OccurredAt: writer.now, Payload: encoded,
	}); err != nil {
		t.Fatal(err)
	}
}

func newP006LocalAPI(t *testing.T, project core.Project) (*httptest.Server, *localapi.Client) {
	t.Helper()
	server := &localapi.Server{Project: project, Sessions: localapi.NewSessionStore(), ControlKey: "scm-e2e-control"}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(func() { _ = server.Close() })
	return httpServer, localapi.NewClient(localcore.Metadata{Endpoint: httpServer.URL, ControlKey: "scm-e2e-control"})
}

func writeP006File(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runP006Git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

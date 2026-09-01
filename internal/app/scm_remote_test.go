package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scmremote"
)

func TestSyncGitHubSCMAppendsOnlyChangedFactsAndNeverCompletesTask(t *testing.T) {
	service, repository, observer, task := newGitHubSCMService(t)
	before, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.SyncGitHubSCM(context.Background(), owner("USR-OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	if report.PullRequests != 1 || report.Checks != 1 || report.Appended != 3 {
		t.Fatalf("report = %#v", report)
	}
	after, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.Tasks[task.ID].Status != before.Tasks[task.ID].Status || after.Tasks[task.ID].Status == model.StatusCompleted {
		t.Fatalf("GitHub sync changed task status from %q to %q", before.Tasks[task.ID].Status, after.Tasks[task.ID].Status)
	}
	count := len(repository.events)
	observer.sync.Refs[0].ObservedAt = observer.sync.Refs[0].ObservedAt.Add(time.Minute)
	observer.sync.PullRequests[0].ObservedAt = observer.sync.PullRequests[0].ObservedAt.Add(time.Minute)
	observer.sync.Checks[0].ObservedAt = observer.sync.Checks[0].ObservedAt.Add(time.Minute)
	second, err := service.SyncGitHubSCM(context.Background(), reviewer("USR-REVIEWER"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Appended != 0 || len(repository.events) != count {
		t.Fatalf("identical sync appended events: report=%#v before=%d after=%d", second, count, len(repository.events))
	}
	if observer.commitSyncCalls != 2 {
		t.Fatalf("CommitSync() calls = %d, want 2", observer.commitSyncCalls)
	}
}

func TestGitHubSCMConnectAndSyncAuthorization(t *testing.T) {
	service, repository, observer, _ := newGitHubSCMServiceWithoutConnect(t)
	before := len(repository.events)
	if _, err := service.ConnectGitHubSCM(context.Background(), reviewer("USR-REVIEWER")); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("reviewer ConnectGitHubSCM() error = %v", err)
	}
	if len(repository.events) != before || observer.commitConnectCalls != 0 {
		t.Fatal("rejected connect changed state")
	}
	if _, err := service.ConnectGitHubSCM(context.Background(), owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SyncGitHubSCM(context.Background(), agent("AGT-BUILD")); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("agent SyncGitHubSCM() error = %v", err)
	}
	if _, err := service.SyncGitHubSCM(context.Background(), model.Actor{ID: "USR-LEAD", Kind: model.ActorHuman, Role: model.RoleLead}); err != nil {
		t.Fatalf("lead SyncGitHubSCM() error = %v", err)
	}
}

func TestGitHubSCMCursorCommitFailureKeepsAppendedFacts(t *testing.T) {
	service, _, observer, _ := newGitHubSCMService(t)
	observer.commitSyncErr = errors.New("cursor disk unavailable")
	if _, err := service.SyncGitHubSCM(context.Background(), owner("USR-OWNER")); !errors.Is(err, ErrOperational) {
		t.Fatalf("SyncGitHubSCM() error = %v", err)
	}
	state, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.SCMPullRequests) != 1 || len(state.SCMChecks) != 1 {
		t.Fatalf("appended facts were rolled back: pulls=%d checks=%d", len(state.SCMPullRequests), len(state.SCMChecks))
	}
}

func newGitHubSCMService(t *testing.T) (*Service, *memoryRepository, *stubRemoteSCMObserver, model.Task) {
	t.Helper()
	service, repository, observer, task := newGitHubSCMServiceWithoutConnect(t)
	if _, err := service.ConnectGitHubSCM(context.Background(), owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	return service, repository, observer, task
}

func newGitHubSCMServiceWithoutConnect(t *testing.T) (*Service, *memoryRepository, *stubRemoteSCMObserver, model.Task) {
	t.Helper()
	service, repository := newWorkflowService(t)
	requirement, tasks, err := service.Plan(context.Background(), PlanInput{
		Title: "Observe GitHub", Tasks: []TaskInput{{Title: "Read remote facts", AcceptanceCriteria: []string{"facts are replayable"}}}, Actor: owner("USR-OWNER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), requirement.ID, owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	local := model.SCMRepository{
		ID: "SCM-001", ProjectID: "PRJ-TEST", Provider: "local-git", ObjectFormat: "sha1", RemoteFingerprint: strings.Repeat("1", 64), RegisteredAt: testTime,
	}
	root := t.TempDir()
	if err := service.ConfigureSCM(&stubSCMInspector{repository: local}, root); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterSCM(context.Background(), owner("USR-OWNER")); err != nil {
		t.Fatal(err)
	}
	remote := model.SCMRemote{
		ID: "RSCM-001", RepositoryID: local.ID, Provider: "github", ProviderRepositoryFingerprint: strings.Repeat("2", 64),
		APIHostSHA256: strings.Repeat("3", 64), RegisteredAt: testTime.Add(time.Minute),
	}
	mergedAt := testTime.Add(3 * time.Minute)
	observer := &stubRemoteSCMObserver{
		connect: scmremote.PreparedConnect{Remote: remote, CommitToken: "connect-token"},
		sync: scmremote.PreparedSync{
			Remote: remote, CommitToken: "sync-token", Runtime: scmremote.RuntimeStatus{Connected: true, LastSuccessfulSync: testTime.Add(5 * time.Minute)},
			Refs: []model.SCMRemoteRefObservation{{RemoteID: remote.ID, Ref: "refs/heads/main", CommitOID: strings.Repeat("a", 40), Change: "created", ObservedAt: testTime.Add(2 * time.Minute)}},
			PullRequests: []model.SCMPullRequestObservation{{
				RemoteID: remote.ID, Number: 1, State: "closed", TitleSHA256: strings.Repeat("4", 64), AuthorSHA256: strings.Repeat("5", 64),
				BaseRef: "refs/heads/main", BaseOID: strings.Repeat("a", 40), HeadRef: "refs/heads/change", HeadRepositorySHA256: strings.Repeat("6", 64),
				HeadOID: strings.Repeat("b", 40), CommitOIDs: []string{strings.Repeat("b", 40)}, MergeCommitOID: strings.Repeat("c", 40),
				MergedAt: &mergedAt, GitHubUpdatedAt: mergedAt, ObservedAt: mergedAt.Add(time.Minute),
			}},
			Checks: []model.SCMCheckObservation{{
				RemoteID: remote.ID, ExternalID: "check-run:9", Source: "check-run", CommitOID: strings.Repeat("b", 40), Name: "ci",
				Status: "completed", Conclusion: "success", CompletedAt: timePointer(testTime.Add(4 * time.Minute)), ObservedAt: testTime.Add(5 * time.Minute),
			}},
		},
	}
	if err := service.ConfigureRemoteSCM(observer, root); err != nil {
		t.Fatal(err)
	}
	return service, repository, observer, tasks[0]
}

type stubRemoteSCMObserver struct {
	connect            scmremote.PreparedConnect
	sync               scmremote.PreparedSync
	commitConnectCalls int
	commitSyncCalls    int
	commitSyncErr      error
}

func (observer *stubRemoteSCMObserver) PrepareConnect(context.Context, string, model.SCMRepository) (scmremote.PreparedConnect, error) {
	return observer.connect, nil
}

func (observer *stubRemoteSCMObserver) CommitConnect(context.Context, string) error {
	observer.commitConnectCalls++
	return nil
}

func (observer *stubRemoteSCMObserver) PrepareSync(context.Context, string) (scmremote.PreparedSync, error) {
	return observer.sync, nil
}

func (observer *stubRemoteSCMObserver) CommitSync(context.Context, string) error {
	observer.commitSyncCalls++
	return observer.commitSyncErr
}

func (observer *stubRemoteSCMObserver) Abort(context.Context, string) {}

func (observer *stubRemoteSCMObserver) RuntimeStatus(context.Context) (scmremote.RuntimeStatus, error) {
	return observer.sync.Runtime, nil
}

func timePointer(value time.Time) *time.Time { return &value }

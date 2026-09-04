package githubscm

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scm"
	"github.com/haochase/haowork/internal/scmremote"
)

const (
	maxPullsPerSync   = 500
	maxReviewsPerSync = 1000
	maxChecksPerSync  = 1000
	defaultLookback   = 90
	defaultOverlap    = 24 * time.Hour
	defaultSyncLimit  = 2 * time.Minute
)

type Observer struct {
	client *Client
	store  *FileStore
	runner scm.Runner
	root   string
	clock  func() time.Time

	pendingMu sync.Mutex
	pending   map[string]pendingRuntimeState
}

type pendingRuntimeState struct {
	config *Config
	cursor *Cursor
}

func NewObserver(client *Client, store *FileStore, runner scm.Runner, root string, clock func() time.Time) *Observer {
	if clock == nil {
		clock = time.Now
	}
	return &Observer{client: client, store: store, runner: runner, root: filepath.Clean(root), clock: clock, pending: make(map[string]pendingRuntimeState)}
}

func (observer *Observer) PrepareConnect(ctx context.Context, root string, local model.SCMRepository) (scmremote.PreparedConnect, error) {
	if err := observer.validate(); err != nil {
		return scmremote.PreparedConnect{}, err
	}
	if local.Provider != "local-git" || strings.TrimSpace(local.ID) == "" || strings.TrimSpace(local.RemoteFingerprint) == "" {
		return scmremote.PreparedConnect{}, errors.New("local SCM repository identity is required")
	}
	if !sameRuntimeRoot(observer.root, root) {
		return scmremote.PreparedConnect{}, errors.New("GitHub observer root does not match project root")
	}
	origin, err := DiscoverOrigin(ctx, observer.runner, root)
	if err != nil {
		return scmremote.PreparedConnect{}, err
	}
	if origin.RemoteFingerprint != local.RemoteFingerprint {
		return scmremote.PreparedConnect{}, errors.New("github_identity_mismatch")
	}
	repositoryResult, err := observer.client.Repository(ctx, origin.Owner, origin.Repository, "")
	if err != nil {
		return scmremote.PreparedConnect{}, err
	}
	expectedFullName := origin.Owner + "/" + origin.Repository
	if !strings.EqualFold(repositoryResult.Repository.FullName, expectedFullName) {
		return scmremote.PreparedConnect{}, errors.New("github_identity_mismatch")
	}
	now := observer.clock().UTC()
	remote := model.SCMRemote{
		ID: stableRemoteID(local.ID, repositoryResult.Repository.ID), RepositoryID: local.ID, Provider: "github",
		ProviderRepositoryFingerprint: digestString("github:" + strconv.FormatInt(repositoryResult.Repository.ID, 10)),
		APIHostSHA256:                 digestString("api.github.com"), RegisteredAt: now,
	}
	config := Config{
		LocalRepositoryID: local.ID, RemoteID: remote.ID, Owner: origin.Owner, Repository: origin.Repository,
		GitHubRepositoryID: repositoryResult.Repository.ID, MonitoredRefs: []string{"refs/heads/" + repositoryResult.Repository.DefaultBranch},
		InitialLookbackDays: defaultLookback, RegisteredAt: now,
	}
	if existing, loadErr := observer.store.LoadConfig(ctx); loadErr == nil {
		if !sameConfigIdentity(existing, config) {
			return scmremote.PreparedConnect{}, errors.New("github_identity_mismatch")
		}
		config = existing
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return scmremote.PreparedConnect{}, loadErr
	}
	token, err := observer.putPending(pendingRuntimeState{config: &config})
	if err != nil {
		return scmremote.PreparedConnect{}, err
	}
	return scmremote.PreparedConnect{Remote: remote, CommitToken: token}, nil
}

func (observer *Observer) CommitConnect(ctx context.Context, token string) error {
	state, ok := observer.pendingState(token)
	if !ok || state.config == nil {
		return errors.New("GitHub connect token is unknown")
	}
	if err := observer.store.SaveConfig(ctx, *state.config); err != nil {
		return err
	}
	observer.deletePending(token)
	return nil
}

func (observer *Observer) PrepareSync(ctx context.Context, remoteID string) (scmremote.PreparedSync, error) {
	if err := observer.validate(); err != nil {
		return scmremote.PreparedSync{}, err
	}
	config, err := observer.store.LoadConfig(ctx)
	if err != nil {
		return scmremote.PreparedSync{}, err
	}
	if config.RemoteID != strings.TrimSpace(remoteID) {
		return scmremote.PreparedSync{}, errors.New("github_identity_mismatch")
	}
	cursor, err := observer.store.LoadCursor(ctx)
	if errors.Is(err, os.ErrNotExist) {
		cursor = Cursor{
			GitHubRepositoryID: config.GitHubRepositoryID, ETags: make(map[string]string), RefOIDs: make(map[string]string), ActivePullHeads: make(map[int]string),
		}
	} else if err != nil {
		return scmremote.PreparedSync{}, fmt.Errorf("github_cursor_invalid: %w", err)
	}
	if cursor.GitHubRepositoryID != config.GitHubRepositoryID {
		return scmremote.PreparedSync{}, errors.New("github_identity_mismatch")
	}
	if cursor.ETags == nil {
		cursor.ETags = make(map[string]string)
	}
	if cursor.RefOIDs == nil {
		cursor.RefOIDs = make(map[string]string)
	}
	if cursor.ActivePullHeads == nil {
		cursor.ActivePullHeads = make(map[int]string)
	}

	syncCtx, cancel := context.WithTimeout(ctx, defaultSyncLimit)
	defer cancel()
	now := observer.clock().UTC()
	remote := remoteFromConfig(config)
	prepared := scmremote.PreparedSync{Remote: remote, Runtime: scmremote.RuntimeStatus{Connected: true, LastSuccessfulSync: cursor.LastSuccessfulSync, RateLimitRemaining: -1}}
	if observer.client.tokens != nil {
		if token, tokenErr := observer.client.tokens.Token(syncCtx); tokenErr != nil {
			return scmremote.PreparedSync{}, tokenErr
		} else {
			prepared.Runtime.Authenticated = strings.TrimSpace(token) != ""
		}
	}

	repositoryKey := "repository"
	repositoryResult, err := observer.client.Repository(syncCtx, config.Owner, config.Repository, cursor.ETags[repositoryKey])
	if err != nil {
		return scmremote.PreparedSync{}, err
	}
	updateMeta(&cursor, repositoryKey, repositoryResult.Meta, &prepared.Runtime)
	if !repositoryResult.Meta.NotModified && repositoryResult.Repository.ID != config.GitHubRepositoryID {
		return scmremote.PreparedSync{}, errors.New("github_identity_mismatch")
	}

	for _, ref := range config.MonitoredRefs {
		refKey := "ref:" + ref
		result, refErr := observer.client.Reference(syncCtx, config.Owner, config.Repository, strings.TrimPrefix(ref, "refs/"), cursor.ETags[refKey])
		if refErr != nil {
			var apiErr *APIError
			if errors.As(refErr, &apiErr) && apiErr.Code == "remote_not_found_or_forbidden" && cursor.RefOIDs[ref] != "" {
				prepared.Refs = append(prepared.Refs, model.SCMRemoteRefObservation{RemoteID: remote.ID, Ref: ref, PreviousOID: cursor.RefOIDs[ref], Change: "deleted", ObservedAt: now})
				delete(cursor.RefOIDs, ref)
				continue
			}
			return scmremote.PreparedSync{}, refErr
		}
		updateMeta(&cursor, refKey, result.Meta, &prepared.Runtime)
		if result.Meta.NotModified {
			continue
		}
		previous := cursor.RefOIDs[ref]
		current := result.Reference.Object.SHA
		if previous == "" {
			prepared.Refs = append(prepared.Refs, model.SCMRemoteRefObservation{RemoteID: remote.ID, Ref: ref, CommitOID: current, Change: "created", ObservedAt: now})
		} else if previous != current {
			prepared.Refs = append(prepared.Refs, model.SCMRemoteRefObservation{RemoteID: remote.ID, Ref: ref, CommitOID: current, PreviousOID: previous, Change: "moved", ObservedAt: now})
		}
		cursor.RefOIDs[ref] = current
	}

	cutoff := now.Add(-time.Duration(config.InitialLookbackDays) * 24 * time.Hour)
	if !cursor.LastSuccessfulSync.IsZero() {
		cutoff = cursor.LastSuccessfulSync.Add(-defaultOverlap)
	}
	activePulls := make(map[int]string, len(cursor.ActivePullHeads))
	for number, oid := range cursor.ActivePullHeads {
		activePulls[number] = oid
	}
	processedCheckOIDs := make(map[string]struct{})
	for page := 1; ; page++ {
		key := fmt.Sprintf("pulls:%d", page)
		result, pullErr := observer.client.PullRequests(syncCtx, config.Owner, config.Repository, PullQuery{State: "all", Sort: "updated", Direction: "desc", PerPage: 100, Page: page}, cursor.ETags[key])
		if pullErr != nil {
			return scmremote.PreparedSync{}, pullErr
		}
		updateMeta(&cursor, key, result.Meta, &prepared.Runtime)
		if result.Meta.NotModified {
			break
		}
		for _, pull := range result.Pulls {
			if len(prepared.PullRequests) >= maxPullsPerSync {
				return scmremote.PreparedSync{}, errors.New("github_window_exceeded")
			}
			if pull.State != "open" && pull.UpdatedAt.Before(cutoff) {
				continue
			}
			pullFacts, reviews, checks, collectErr := observer.collectPull(syncCtx, config, &cursor, remote.ID, pull, now, &prepared.Runtime)
			if collectErr != nil {
				return scmremote.PreparedSync{}, collectErr
			}
			prepared.PullRequests = append(prepared.PullRequests, pullFacts)
			prepared.Reviews = append(prepared.Reviews, reviews...)
			prepared.Checks = append(prepared.Checks, checks...)
			processedCheckOIDs[pullFacts.HeadOID] = struct{}{}
			if pullFacts.MergeCommitOID != "" {
				processedCheckOIDs[pullFacts.MergeCommitOID] = struct{}{}
			}
			if len(prepared.Reviews) > maxReviewsPerSync || len(prepared.Checks) > maxChecksPerSync {
				return scmremote.PreparedSync{}, errors.New("github_window_exceeded")
			}
			if pullFacts.State == "open" {
				activePulls[pullFacts.Number] = pullFacts.HeadOID
			} else {
				delete(activePulls, pullFacts.Number)
			}
		}
		if result.Meta.NextURL == "" {
			break
		}
	}
	activeNumbers := make([]int, 0, len(activePulls))
	for number := range activePulls {
		activeNumbers = append(activeNumbers, number)
	}
	sort.Ints(activeNumbers)
	for _, number := range activeNumbers {
		oid := activePulls[number]
		if _, exists := processedCheckOIDs[oid]; exists {
			continue
		}
		checks, checkErr := observer.collectChecks(syncCtx, config, &cursor, remote.ID, oid, now, &prepared.Runtime)
		if checkErr != nil {
			return scmremote.PreparedSync{}, checkErr
		}
		prepared.Checks = append(prepared.Checks, checks...)
		if len(prepared.Checks) > maxChecksPerSync {
			return scmremote.PreparedSync{}, errors.New("github_window_exceeded")
		}
	}
	cursor.ActivePullHeads = activePulls
	cursor.LastSuccessfulSync = now
	cursor.OverlapSince = now.Add(-defaultOverlap)
	prepared.Runtime.LastSuccessfulSync = now
	token, err := observer.putPending(pendingRuntimeState{cursor: &cursor})
	if err != nil {
		return scmremote.PreparedSync{}, err
	}
	prepared.CommitToken = token
	sortPrepared(&prepared)
	return prepared, nil
}

func (observer *Observer) CommitSync(ctx context.Context, token string) error {
	state, ok := observer.pendingState(token)
	if !ok || state.cursor == nil {
		return errors.New("GitHub sync token is unknown")
	}
	if err := observer.store.SaveCursor(ctx, *state.cursor); err != nil {
		return err
	}
	observer.deletePending(token)
	return nil
}

func (observer *Observer) Abort(_ context.Context, token string) {
	observer.deletePending(token)
}

func (observer *Observer) RuntimeStatus(ctx context.Context) (scmremote.RuntimeStatus, error) {
	if err := observer.validate(); err != nil {
		return scmremote.RuntimeStatus{}, err
	}
	if _, err := observer.store.LoadConfig(ctx); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return scmremote.RuntimeStatus{}, nil
		}
		return scmremote.RuntimeStatus{}, err
	}
	status := scmremote.RuntimeStatus{Connected: true, RateLimitRemaining: -1}
	if cursor, err := observer.store.LoadCursor(ctx); err == nil {
		status.LastSuccessfulSync = cursor.LastSuccessfulSync
		status.RetryAt = cursor.RateLimitReset
	}
	if observer.client.tokens != nil {
		token, err := observer.client.tokens.Token(ctx)
		if err != nil {
			return scmremote.RuntimeStatus{}, err
		}
		status.Authenticated = strings.TrimSpace(token) != ""
	}
	return status, nil
}

func (observer *Observer) collectPull(ctx context.Context, config Config, cursor *Cursor, remoteID string, listed PullRequest, now time.Time, runtime *scmremote.RuntimeStatus) (model.SCMPullRequestObservation, []model.SCMReviewObservation, []model.SCMCheckObservation, error) {
	detailKey := fmt.Sprintf("pull:%d", listed.Number)
	detailResult, err := observer.client.PullRequest(ctx, config.Owner, config.Repository, listed.Number, cursor.ETags[detailKey])
	if err != nil {
		return model.SCMPullRequestObservation{}, nil, nil, err
	}
	updateMeta(cursor, detailKey, detailResult.Meta, runtime)
	pull := detailResult.Pull
	if detailResult.Meta.NotModified {
		pull = listed
	}
	commits, err := observer.collectCommits(ctx, config, cursor, pull.Number, runtime)
	if err != nil {
		return model.SCMPullRequestObservation{}, nil, nil, err
	}
	reviews, err := observer.collectReviews(ctx, config, cursor, remoteID, pull.Number, now, runtime)
	if err != nil {
		return model.SCMPullRequestObservation{}, nil, nil, err
	}
	checks, err := observer.collectChecks(ctx, config, cursor, remoteID, pull.Head.SHA, now, runtime)
	if err != nil {
		return model.SCMPullRequestObservation{}, nil, nil, err
	}
	if pull.MergeCommitSHA != "" && pull.MergeCommitSHA != pull.Head.SHA {
		mergeChecks, mergeErr := observer.collectChecks(ctx, config, cursor, remoteID, pull.MergeCommitSHA, now, runtime)
		if mergeErr != nil {
			return model.SCMPullRequestObservation{}, nil, nil, mergeErr
		}
		checks = append(checks, mergeChecks...)
	}
	commitOIDs := make([]string, 0, len(commits))
	for _, commit := range commits {
		commitOIDs = append(commitOIDs, commit.SHA)
	}
	sort.Strings(commitOIDs)
	return model.SCMPullRequestObservation{
		RemoteID: remoteID, Number: pull.Number, State: pull.State, Draft: pull.Draft,
		TitleSHA256: digestString(pull.Title), AuthorSHA256: digestString(pull.User.Login),
		BaseRef: "refs/heads/" + pull.Base.Ref, BaseOID: pull.Base.SHA, HeadRef: "refs/heads/" + pull.Head.Ref,
		HeadRepositorySHA256: digestString(pull.Head.Repo.FullName), HeadOID: pull.Head.SHA, CommitOIDs: commitOIDs,
		MergeCommitOID: pull.MergeCommitSHA, MergedAt: pull.MergedAt, GitHubUpdatedAt: pull.UpdatedAt, ObservedAt: now,
	}, reviews, checks, nil
}

func (observer *Observer) collectCommits(ctx context.Context, config Config, cursor *Cursor, pullNumber int, runtime *scmremote.RuntimeStatus) ([]PullCommit, error) {
	var commits []PullCommit
	for page := 1; ; page++ {
		key := fmt.Sprintf("pull:%d:commits:%d", pullNumber, page)
		result, err := observer.client.PullCommits(ctx, config.Owner, config.Repository, pullNumber, PullQuery{PerPage: 100, Page: page}, cursor.ETags[key])
		if err != nil {
			return nil, err
		}
		updateMeta(cursor, key, result.Meta, runtime)
		if !result.Meta.NotModified {
			commits = append(commits, result.Commits...)
		}
		if result.Meta.NextURL == "" {
			break
		}
	}
	return commits, nil
}

func (observer *Observer) collectReviews(ctx context.Context, config Config, cursor *Cursor, remoteID string, pullNumber int, now time.Time, runtime *scmremote.RuntimeStatus) ([]model.SCMReviewObservation, error) {
	var observations []model.SCMReviewObservation
	for page := 1; ; page++ {
		key := fmt.Sprintf("pull:%d:reviews:%d", pullNumber, page)
		result, err := observer.client.PullReviews(ctx, config.Owner, config.Repository, pullNumber, PullQuery{PerPage: 100, Page: page}, cursor.ETags[key])
		if err != nil {
			return nil, err
		}
		updateMeta(cursor, key, result.Meta, runtime)
		if !result.Meta.NotModified {
			for _, review := range result.Reviews {
				observations = append(observations, model.SCMReviewObservation{
					RemoteID: remoteID, PullNumber: pullNumber, ReviewID: review.ID, CommitOID: review.CommitID,
					ReviewerSHA256: digestString(review.User.Login), State: review.State, SubmittedAt: review.SubmittedAt, ObservedAt: now,
				})
			}
		}
		if result.Meta.NextURL == "" {
			break
		}
	}
	return observations, nil
}

func (observer *Observer) collectChecks(ctx context.Context, config Config, cursor *Cursor, remoteID, oid string, now time.Time, runtime *scmremote.RuntimeStatus) ([]model.SCMCheckObservation, error) {
	var observations []model.SCMCheckObservation
	for page := 1; ; page++ {
		key := fmt.Sprintf("checks:%s:%d", oid, page)
		result, err := observer.client.CheckRuns(ctx, config.Owner, config.Repository, oid, PullQuery{PerPage: 100, Page: page}, cursor.ETags[key])
		if err != nil {
			return nil, err
		}
		updateMeta(cursor, key, result.Meta, runtime)
		if !result.Meta.NotModified {
			for _, check := range result.CheckRuns {
				observations = append(observations, model.SCMCheckObservation{
					RemoteID: remoteID, ExternalID: "check-run:" + check.HeadSHA + ":" + strconv.FormatInt(check.ID, 10), Source: "check-run", CommitOID: check.HeadSHA,
					Name: check.Name, Status: check.Status, Conclusion: check.Conclusion, StartedAt: check.StartedAt, CompletedAt: check.CompletedAt, ObservedAt: now,
				})
			}
		}
		if result.Meta.NextURL == "" {
			break
		}
	}
	statusKey := "status:" + oid
	statusResult, err := observer.client.CombinedStatus(ctx, config.Owner, config.Repository, oid, cursor.ETags[statusKey])
	if err != nil {
		return nil, err
	}
	updateMeta(cursor, statusKey, statusResult.Meta, runtime)
	if !statusResult.Meta.NotModified {
		for _, status := range statusResult.Status.Statuses {
			checkStatus, conclusion := normalizeCommitStatus(status.State)
			startedAt, completedAt := status.CreatedAt, status.UpdatedAt
			observations = append(observations, model.SCMCheckObservation{
				RemoteID: remoteID, ExternalID: "commit-status:" + oid + ":" + strconv.FormatInt(status.ID, 10), Source: "commit-status", CommitOID: oid,
				Name: status.Context, Status: checkStatus, Conclusion: conclusion, StartedAt: &startedAt, CompletedAt: completedPointer(checkStatus, completedAt), ObservedAt: now,
			})
		}
	}
	return observations, nil
}

func (observer *Observer) validate() error {
	if observer == nil || observer.client == nil || observer.store == nil || observer.runner == nil || strings.TrimSpace(observer.root) == "" || observer.clock == nil {
		return errors.New("GitHub observer is not configured")
	}
	return nil
}

func (observer *Observer) putPending(state pendingRuntimeState) (string, error) {
	observer.pendingMu.Lock()
	defer observer.pendingMu.Unlock()
	if len(observer.pending) >= 64 {
		return "", errors.New("too many pending GitHub runtime updates")
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(bytes)
	observer.pending[token] = state
	return token, nil
}

func (observer *Observer) pendingState(token string) (pendingRuntimeState, bool) {
	observer.pendingMu.Lock()
	defer observer.pendingMu.Unlock()
	state, ok := observer.pending[strings.TrimSpace(token)]
	return state, ok
}

func (observer *Observer) deletePending(token string) {
	observer.pendingMu.Lock()
	defer observer.pendingMu.Unlock()
	delete(observer.pending, strings.TrimSpace(token))
}

func stableRemoteID(repositoryID string, githubID int64) string {
	digest := sha256.Sum256([]byte(repositoryID + "\n" + strconv.FormatInt(githubID, 10)))
	return "RSCM-" + hex.EncodeToString(digest[:12])
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func remoteFromConfig(config Config) model.SCMRemote {
	return model.SCMRemote{
		ID: config.RemoteID, RepositoryID: config.LocalRepositoryID, Provider: "github",
		ProviderRepositoryFingerprint: digestString("github:" + strconv.FormatInt(config.GitHubRepositoryID, 10)),
		APIHostSHA256:                 digestString("api.github.com"), RegisteredAt: config.RegisteredAt,
	}
}

func sameConfigIdentity(left, right Config) bool {
	return left.LocalRepositoryID == right.LocalRepositoryID && left.RemoteID == right.RemoteID && left.GitHubRepositoryID == right.GitHubRepositoryID && strings.EqualFold(left.Owner, right.Owner) && strings.EqualFold(left.Repository, right.Repository)
}

func sameRuntimeRoot(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func updateMeta(cursor *Cursor, key string, meta ResponseMeta, runtime *scmremote.RuntimeStatus) {
	if meta.ETag != "" {
		cursor.ETags[key] = meta.ETag
	}
	if !meta.RateLimitReset.IsZero() {
		cursor.RateLimitReset = meta.RateLimitReset
	}
	if meta.RateLimitRemaining >= 0 {
		runtime.RateLimitRemaining = meta.RateLimitRemaining
	}
}

func normalizeCommitStatus(state string) (string, string) {
	if state == "pending" {
		return "pending", ""
	}
	return "completed", state
}

func completedPointer(status string, value time.Time) *time.Time {
	if status != "completed" {
		return nil
	}
	return &value
}

func sortPrepared(prepared *scmremote.PreparedSync) {
	sort.Slice(prepared.Refs, func(left, right int) bool { return prepared.Refs[left].Ref < prepared.Refs[right].Ref })
	sort.Slice(prepared.PullRequests, func(left, right int) bool {
		return prepared.PullRequests[left].Number < prepared.PullRequests[right].Number
	})
	sort.Slice(prepared.Reviews, func(left, right int) bool { return prepared.Reviews[left].ReviewID < prepared.Reviews[right].ReviewID })
	sort.Slice(prepared.Checks, func(left, right int) bool {
		if prepared.Checks[left].CommitOID != prepared.Checks[right].CommitOID {
			return prepared.Checks[left].CommitOID < prepared.Checks[right].CommitOID
		}
		return prepared.Checks[left].ExternalID < prepared.Checks[right].ExternalID
	})
}

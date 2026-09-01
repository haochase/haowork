package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scmremote"
)

type RemoteSCMSyncReport struct {
	Refs         int       `json:"refs"`
	PullRequests int       `json:"pull_requests"`
	Reviews      int       `json:"reviews"`
	Checks       int       `json:"checks"`
	Appended     int       `json:"appended"`
	SyncedAt     time.Time `json:"synced_at"`
}

func (service *Service) ConfigureRemoteSCM(observer scmremote.Observer, root string) error {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	root = strings.TrimSpace(root)
	if observer == nil || root == "" {
		return errors.New("remote SCM observer and project root are required")
	}
	if service.scmRemoteObserver != nil || service.scmRemoteRoot != "" {
		return fmt.Errorf("%w: remote SCM observer is already configured", ErrConflict)
	}
	if service.scmRoot != "" && !strings.EqualFold(service.scmRoot, root) {
		return fmt.Errorf("%w: local and remote SCM roots differ", ErrConflict)
	}
	service.scmRemoteObserver = observer
	service.scmRemoteRoot = root
	return nil
}

func (service *Service) ConnectGitHubSCM(ctx context.Context, actor model.Actor) (model.SCMRemote, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if err := service.validateRemoteSCMConfiguration(); err != nil {
		return model.SCMRemote{}, err
	}
	if err := validateActor(actor); err != nil {
		return model.SCMRemote{}, err
	}
	if actor.Kind != model.ActorHuman || actor.Role != model.RoleOwner {
		return model.SCMRemote{}, ErrApprovalRequired
	}
	state, history, err := service.snapshotWithEvents(ctx)
	if err != nil {
		return model.SCMRemote{}, err
	}
	repository, err := oneSCMRepository(state)
	if err != nil {
		return model.SCMRemote{}, err
	}
	prepared, err := service.scmRemoteObserver.PrepareConnect(ctx, service.scmRemoteRoot, repository)
	if err != nil {
		return model.SCMRemote{}, wrapOperational(err)
	}
	committed := false
	defer func() {
		if !committed {
			service.scmRemoteObserver.Abort(context.Background(), prepared.CommitToken)
		}
	}()
	if existing, exists := state.SCMRemotes[prepared.Remote.ID]; exists {
		if !sameSCMRemote(existing, prepared.Remote) {
			return model.SCMRemote{}, fmt.Errorf("%w: remote SCM identity diverged", ErrConflict)
		}
		if err := service.scmRemoteObserver.CommitConnect(ctx, prepared.CommitToken); err != nil {
			return model.SCMRemote{}, wrapOperational(err)
		}
		committed = true
		return existing, nil
	}
	event, err := service.prepareSCMEvent("scm.remote.registered", "scm_remote", prepared.Remote.ID, actor, model.SCMRemoteRegistered{Remote: prepared.Remote})
	if err != nil {
		return model.SCMRemote{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.SCMRemote{}, err
	}
	if err := service.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.SCMRemote{}, err
	}
	if err := service.scmRemoteObserver.CommitConnect(ctx, prepared.CommitToken); err != nil {
		return model.SCMRemote{}, wrapOperational(err)
	}
	committed = true
	return prepared.Remote, nil
}

func (service *Service) SyncGitHubSCM(ctx context.Context, actor model.Actor) (RemoteSCMSyncReport, error) {
	service.mutationMu.Lock()
	defer service.mutationMu.Unlock()
	if err := service.validateRemoteSCMConfiguration(); err != nil {
		return RemoteSCMSyncReport{}, err
	}
	if err := validateActor(actor); err != nil {
		return RemoteSCMSyncReport{}, err
	}
	if actor.Kind != model.ActorHuman || (actor.Role != model.RoleOwner && actor.Role != model.RoleLead && actor.Role != model.RoleReviewer) {
		return RemoteSCMSyncReport{}, ErrApprovalRequired
	}
	state, history, err := service.snapshotWithEvents(ctx)
	if err != nil {
		return RemoteSCMSyncReport{}, err
	}
	remote, err := oneSCMRemote(state)
	if err != nil {
		return RemoteSCMSyncReport{}, err
	}
	prepared, err := service.scmRemoteObserver.PrepareSync(ctx, remote.ID)
	if err != nil {
		return RemoteSCMSyncReport{}, wrapOperational(err)
	}
	committed := false
	defer func() {
		if !committed {
			service.scmRemoteObserver.Abort(context.Background(), prepared.CommitToken)
		}
	}()
	if !sameSCMRemote(prepared.Remote, remote) {
		return RemoteSCMSyncReport{}, fmt.Errorf("%w: prepared remote identity diverged: stored=%#v prepared=%#v", ErrConflict, remote, prepared.Remote)
	}
	report := RemoteSCMSyncReport{
		Refs: len(prepared.Refs), PullRequests: len(prepared.PullRequests), Reviews: len(prepared.Reviews), Checks: len(prepared.Checks),
		SyncedAt: prepared.Runtime.LastSuccessfulSync,
	}
	events, err := service.remoteObservationEvents(state, actor, prepared)
	if err != nil {
		return RemoteSCMSyncReport{}, err
	}
	if len(events) > 0 {
		if err := validateSCMPrepared(history, events...); err != nil {
			return RemoteSCMSyncReport{}, err
		}
		if err := service.appendPreparedEvents(ctx, len(history), events); err != nil {
			return RemoteSCMSyncReport{}, err
		}
	}
	report.Appended = len(events)
	if err := service.scmRemoteObserver.CommitSync(ctx, prepared.CommitToken); err != nil {
		return RemoteSCMSyncReport{}, wrapOperational(err)
	}
	committed = true
	return report, nil
}

func (service *Service) GitHubSCMRuntimeStatus(ctx context.Context) (scmremote.RuntimeStatus, error) {
	if err := service.validateRemoteSCMConfiguration(); err != nil {
		return scmremote.RuntimeStatus{}, err
	}
	status, err := service.scmRemoteObserver.RuntimeStatus(ctx)
	if err != nil {
		return scmremote.RuntimeStatus{}, wrapOperational(err)
	}
	return status, nil
}

func (service *Service) remoteObservationEvents(state model.ProjectState, actor model.Actor, prepared scmremote.PreparedSync) ([]model.Event, error) {
	var events []model.Event
	for _, observation := range prepared.Refs {
		if existing, exists := state.SCMRemoteRefs[model.SCMRemoteRefKey(observation.RemoteID, observation.Ref)]; exists {
			same, err := sameRemoteFact(existing, observation)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		event, err := service.prepareSCMEvent("scm.remote.ref.observed", "scm_remote_ref", model.SCMRemoteRefKey(observation.RemoteID, observation.Ref), actor, model.SCMRemoteRefObserved{Observation: observation})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, observation := range prepared.PullRequests {
		if existing, exists := state.SCMPullRequests[model.SCMRemotePullKey(observation.RemoteID, observation.Number)]; exists {
			same, err := sameRemoteFact(existing, observation)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		event, err := service.prepareSCMEvent("scm.remote.pull_request.observed", "scm_remote_pull", model.SCMRemotePullKey(observation.RemoteID, observation.Number), actor, model.SCMRemotePullRequestObserved{Observation: observation})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, observation := range prepared.Reviews {
		if existing, exists := state.SCMReviews[model.SCMRemoteReviewKey(observation.RemoteID, observation.ReviewID)]; exists {
			same, err := sameRemoteFact(existing, observation)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		event, err := service.prepareSCMEvent("scm.remote.review.observed", "scm_remote_review", model.SCMRemoteReviewKey(observation.RemoteID, observation.ReviewID), actor, model.SCMRemoteReviewObserved{Observation: observation})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	for _, observation := range prepared.Checks {
		if existing, exists := state.SCMChecks[model.SCMRemoteCheckKey(observation.RemoteID, observation.ExternalID)]; exists {
			same, err := sameRemoteFact(existing, observation)
			if err != nil {
				return nil, err
			}
			if same {
				continue
			}
		}
		event, err := service.prepareSCMEvent("scm.remote.check.observed", "scm_remote_check", model.SCMRemoteCheckKey(observation.RemoteID, observation.ExternalID), actor, model.SCMRemoteCheckObserved{Observation: observation})
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (service *Service) validateRemoteSCMConfiguration() error {
	if service.scmRemoteObserver == nil || strings.TrimSpace(service.scmRemoteRoot) == "" {
		return wrapOperational(errors.New("remote SCM observation is not configured"))
	}
	return nil
}

func oneSCMRepository(state model.ProjectState) (model.SCMRepository, error) {
	ids := make([]string, 0, len(state.SCMRepositories))
	for id := range state.SCMRepositories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) != 1 {
		return model.SCMRepository{}, fmt.Errorf("%w: exactly one local SCM repository is required", ErrConflict)
	}
	return state.SCMRepositories[ids[0]], nil
}

func oneSCMRemote(state model.ProjectState) (model.SCMRemote, error) {
	ids := make([]string, 0, len(state.SCMRemotes))
	for id := range state.SCMRemotes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) != 1 {
		return model.SCMRemote{}, fmt.Errorf("%w: exactly one GitHub remote is required", ErrConflict)
	}
	return state.SCMRemotes[ids[0]], nil
}

func sameSCMRemote(left, right model.SCMRemote) bool {
	return left.ID == right.ID && left.RepositoryID == right.RepositoryID && left.Provider == right.Provider &&
		left.ProviderRepositoryFingerprint == right.ProviderRepositoryFingerprint && left.APIHostSHA256 == right.APIHostSHA256 &&
		left.RegisteredAt.Equal(right.RegisteredAt)
}

func sameRemoteFact(left, right any) (bool, error) {
	leftJSON, err := json.Marshal(remoteFactContent(left))
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(remoteFactContent(right))
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func remoteFactContent(value any) any {
	switch typed := value.(type) {
	case model.SCMRemoteRefObservation:
		typed.ObservedAt = time.Time{}
		return typed
	case model.SCMPullRequestObservation:
		typed.ObservedAt = time.Time{}
		return typed
	case model.SCMReviewObservation:
		typed.ObservedAt = time.Time{}
		return typed
	case model.SCMCheckObservation:
		typed.ObservedAt = time.Time{}
		return typed
	default:
		return value
	}
}

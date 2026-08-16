package team

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

func TestRecoverRebuildsDeletedIndexFromAcceptedFacts(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	index := &recordingIndex{}
	service := newTeamService(t, root, Dependencies{Index: index})
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	event := testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-1", "GCH-1")
	if _, err := service.Push(ctx, principal, PushBatch{BatchID: "BATCH-1", BaseTeamSeq: 1, Events: []model.Event{event}}); err != nil {
		t.Fatal(err)
	}
	index.deleted = true
	index.events = nil

	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	accepted := readTeamAccepted(t, root)
	if index.deleted || !reflect.DeepEqual(index.events, accepted) {
		t.Fatalf("Recover() index events = %#v, want accepted facts %#v", index.events, accepted)
	}
}

func TestMaterializerRejectsCanonicalHistoryThatIsNotAcceptedPrefix(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	if err := ensureAcceptedLog(root); err != nil {
		t.Fatal(err)
	}
	accepted := readMaterialized(t, root)
	if _, err := teamAcceptedStore(root).ImportAcceptedBatch(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	principal := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	if _, err := eventstore.New(root).Append(ctx, testGoalProposalEvent(t, principal, "BATCH-1", 1, "EVT-GCH-1", "GCH-1")); err != nil {
		t.Fatal(err)
	}

	// A second canonical event makes canonical history longer than accepted history,
	// which a recovery pass must treat as divergence rather than silently overwrite.
	if _, err := New(ctx, root, testDependencies()); !errors.Is(err, ErrHistoryDivergent) {
		t.Fatalf("New() error = %v, want accepted-prefix divergence error", err)
	}
}

func TestRecoverRecreatesMissingCanonicalHistoryFromAcceptedFacts(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)
	if err := os.Remove(filepath.Join(root, ".haowork", "events.jsonl")); err != nil {
		t.Fatal(err)
	}

	if err := service.Recover(ctx); err != nil {
		t.Fatalf("Recover() = %v, want accepted facts to recreate the missing canonical log", err)
	}
	accepted := readTeamAccepted(t, root)
	if canonical := readMaterialized(t, root); !reflect.DeepEqual(canonical, accepted) {
		t.Fatalf("recovered canonical history = %#v, want accepted facts %#v", canonical, accepted)
	}
	event := testGoalProposalEvent(t, owner, "BATCH-AFTER-FAILURE", 1, "EVT-AFTER-FAILURE", "GCH-AFTER-FAILURE")
	result, err := service.Push(ctx, owner, PushBatch{BatchID: "BATCH-AFTER-FAILURE", BaseTeamSeq: 1, Events: []model.Event{event}})
	if err != nil || result.Status != PushAccepted || !result.Materialized {
		t.Fatalf("Push() = %#v, %v, want writable service after canonical recovery", result, err)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 2 {
		t.Fatalf("accepted event count = %d, want append after successful recovery", len(accepted))
	}
}

func TestRecoverFailureMakesPreviouslyWritableServiceReadOnly(t *testing.T) {
	ctx := context.Background()
	root := newInitializedTeamRoot(t)
	service := newTeamService(t, root, Dependencies{})
	service.materializer = &switchableMaterializer{delegate: service.materializer, recoverErr: errors.New("injected recovery failure")}
	owner := testPrincipal("USR-OWNER", model.ActorHuman, model.RoleOwner)

	if err := service.Recover(ctx); err == nil {
		t.Fatal("Recover() succeeded despite injected materializer failure")
	}
	event := testGoalProposalEvent(t, owner, "BATCH-AFTER-FAILURE", 1, "EVT-AFTER-FAILURE", "GCH-AFTER-FAILURE")
	if _, err := service.Push(ctx, owner, PushBatch{BatchID: "BATCH-AFTER-FAILURE", BaseTeamSeq: 1, Events: []model.Event{event}}); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Push() error = %v, want read-only service after failed recovery", err)
	}
	if accepted := readTeamAccepted(t, root); len(accepted) != 1 {
		t.Fatalf("accepted event count = %d, want no append after failed recovery", len(accepted))
	}
}

type recordingIndex struct {
	deleted bool
	events  []model.Event
	err     error
}

func (index *recordingIndex) Rebuild(_ context.Context, events []model.Event) error {
	if index.err != nil {
		return index.err
	}
	index.deleted = false
	index.events = append([]model.Event(nil), events...)
	return nil
}

type switchableMaterializer struct {
	delegate   Materializer
	fail       bool
	recoverErr error
}

func testDependencies() Dependencies {
	return Dependencies{Clock: fixedTeamClock{value: teamTestTime}, IDs: &teamIDs{}}
}

func (materializer *switchableMaterializer) Materialize(ctx context.Context, accepted []model.Event) error {
	if materializer.fail {
		return errors.New("injected materialization failure")
	}
	return materializer.delegate.Materialize(ctx, accepted)
}

func (materializer *switchableMaterializer) Recover(ctx context.Context, accepted []model.Event) error {
	if materializer.recoverErr != nil {
		return materializer.recoverErr
	}
	return materializer.delegate.Recover(ctx, accepted)
}

func (materializer *switchableMaterializer) MaterializedThrough(ctx context.Context) (uint64, error) {
	return materializer.delegate.MaterializedThrough(ctx)
}

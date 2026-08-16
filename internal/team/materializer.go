package team

import (
	"context"
	"fmt"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

// FileMaterializer derives portable canonical facts and the disposable local
// index from the accepted team history.
type FileMaterializer struct {
	root         string
	materialized eventstore.Store
	index        IndexRebuilder
}

func NewFileMaterializer(root string, materialized eventstore.Store, index IndexRebuilder) *FileMaterializer {
	return &FileMaterializer{root: root, materialized: materialized, index: index}
}

func (materializer *FileMaterializer) Materialize(ctx context.Context, accepted []model.Event) error {
	return materializer.apply(ctx, accepted)
}

func (materializer *FileMaterializer) Recover(ctx context.Context, accepted []model.Event) error {
	return materializer.apply(ctx, accepted)
}

func (materializer *FileMaterializer) MaterializedThrough(ctx context.Context) (uint64, error) {
	events, err := materializer.materialized.ReadAll(ctx)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].Sequence, nil
}

func (materializer *FileMaterializer) apply(ctx context.Context, accepted []model.Event) error {
	if err := ensureCanonicalLog(materializer.root); err != nil {
		return fmt.Errorf("ensure canonical event log: %w", err)
	}
	if len(accepted) > 0 {
		if _, err := materializer.materialized.ImportAcceptedBatch(ctx, accepted); err != nil {
			return fmt.Errorf("import accepted facts: %w", err)
		}
		state, err := model.Reduce(accepted)
		if err != nil {
			return fmt.Errorf("reduce accepted facts for materialization: %w", err)
		}
		if err := materializer.updateManifestGoalVersion(state.Goal.Version); err != nil {
			return err
		}
	}
	if materializer.index != nil {
		if err := materializer.index.Rebuild(ctx, accepted); err != nil {
			return fmt.Errorf("rebuild runtime index: %w", err)
		}
	}
	return nil
}

func (materializer *FileMaterializer) updateManifestGoalVersion(acceptedVersion int) error {
	if acceptedVersion == 0 {
		return nil
	}
	manifest, err := capsule.Load(materializer.root)
	if err != nil {
		return fmt.Errorf("load manifest before goal materialization: %w", err)
	}
	if manifest.CurrentGoalVersion > acceptedVersion {
		return fmt.Errorf("%w: manifest goal version %d exceeds accepted version %d", ErrMaterialization, manifest.CurrentGoalVersion, acceptedVersion)
	}
	for version := manifest.CurrentGoalVersion; version < acceptedVersion; version++ {
		if err := capsule.UpdateGoalVersion(materializer.root, version, version+1); err != nil {
			return fmt.Errorf("advance manifest goal version: %w", err)
		}
	}
	return nil
}

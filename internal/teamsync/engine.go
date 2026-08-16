package teamsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

var (
	// ErrOffline reports an operational remote failure without changing local
	// pending work. Callers may schedule another one-shot Sync later.
	ErrOffline = errors.New("team remote is offline")
	// ErrStaleCursor reports that a fresh pull/rebase is required before this
	// pending batch can be sent again.
	ErrStaleCursor = errors.New("team cursor is stale")
)

const invalidLocalTransitionCode = "invalid_local_transition"

// Remote is the small Team Core surface required by the pull-first engine.
// teamapi.Client implements it directly.
type Remote interface {
	Pull(context.Context, uint64) ([]model.Event, error)
	Push(context.Context, team.PushBatch) (team.PushResult, error)
	Status(context.Context) (team.Status, error)
}

// SyncReport describes one deterministic synchronisation attempt. Results are
// ordered by the same outbox order used for sends and terminal reconciliation.
type SyncReport struct {
	Cursor                                uint64
	Pulled, Accepted, Rejected, Conflicts int
	Pending                               int
	Results                               []team.PushResult
}

// Engine has no retry loop. It is safe for a CLI, Local API, or scheduler to
// create an engine and invoke Sync once per explicit attempt.
type Engine struct {
	root     string
	remote   Remote
	accepted eventstore.Store
	outbox   Outbox
	config   ClientConfig
}

func NewEngine(root string, remote Remote, accepted eventstore.Store, config ClientConfig) *Engine {
	return &Engine{root: root, remote: remote, accepted: accepted, outbox: NewOutbox(root, config.DeviceID), config: config}
}

func (engine *Engine) Sync(ctx context.Context) (SyncReport, error) {
	if engine == nil {
		return SyncReport{}, errors.New("team sync engine is not configured")
	}
	report := SyncReport{Cursor: engine.config.Cursor}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if engine.remote == nil {
		return report, errors.New("team sync engine is not configured")
	}

	status, err := engine.remote.Status(ctx)
	if err != nil {
		return report, remoteError(ctx, err)
	}
	if status.ProjectID != engine.config.TeamProjectID {
		return report, fmt.Errorf("team project %q does not match local project %q", status.ProjectID, engine.config.TeamProjectID)
	}
	if !status.Writable {
		return report, team.ErrNotWritable
	}

	pulled, err := engine.remote.Pull(ctx, engine.config.Cursor)
	if err != nil {
		return report, remoteError(ctx, err)
	}
	report.Pulled = len(pulled)
	if len(pulled) > 0 {
		if _, err := engine.accepted.ImportAcceptedBatch(ctx, pulled); err != nil {
			return report, fmt.Errorf("import pulled accepted events: %w", err)
		}
	}

	// Reconciliation must precede cursor persistence. A cancellation after an
	// import therefore re-pulls idempotently instead of hiding an accepted
	// event behind an advanced cursor while its outbox entry remains pending.
	if err := engine.reconcilePulled(ctx, pulled, &report); err != nil {
		return report, err
	}
	if len(pulled) > 0 {
		engine.config.Cursor = pulled[len(pulled)-1].Sequence
		if err := SaveConfig(ctx, engine.root, engine.config); err != nil {
			return report, fmt.Errorf("persist team cursor: %w", err)
		}
		report.Cursor = engine.config.Cursor
	}

	pending, err := engine.preflightPending(ctx, &report)
	if err != nil {
		return report, err
	}
	for _, entry := range pending {
		if err := ctx.Err(); err != nil {
			return engine.finishReport(ctx, report), err
		}
		result, pushErr := engine.remote.Push(ctx, entry.Batch)
		if result.Status == team.PushConflict && result.Code == team.CodeStaleBaseline {
			report.Results = append(report.Results, result)
			if err := engine.outbox.RecordRetryableResult(ctx, entry.Batch.BatchID, result); err != nil {
				return engine.finishReport(ctx, report), fmt.Errorf("persist retryable stale result: %w", err)
			}
			report.Conflicts++
			return engine.finishReport(ctx, report), ErrStaleCursor
		}
		if pushErr != nil {
			return engine.finishReport(ctx, report), remoteError(ctx, pushErr)
		}
		report.Results = append(report.Results, result)
		if result.Status == team.PushAccepted {
			if len(result.Events) == 0 {
				return engine.finishReport(ctx, report), errors.New("accepted team result has no events")
			}
			if _, err := engine.accepted.ImportAcceptedBatch(ctx, result.Events); err != nil {
				return engine.finishReport(ctx, report), fmt.Errorf("import pushed accepted events: %w", err)
			}
		}
		if err := engine.outbox.Update(ctx, entry.Batch.BatchID, result); err != nil {
			return engine.finishReport(ctx, report), fmt.Errorf("persist team result: %w", err)
		}
		switch result.Status {
		case team.PushAccepted:
			report.Accepted++
			if result.TeamSeqTo > engine.config.Cursor {
				engine.config.Cursor = result.TeamSeqTo
				if err := SaveConfig(ctx, engine.root, engine.config); err != nil {
					return engine.finishReport(ctx, report), fmt.Errorf("persist team cursor: %w", err)
				}
				report.Cursor = engine.config.Cursor
			}
		case team.PushRejected:
			report.Rejected++
		case team.PushConflict:
			report.Conflicts++
		default:
			return engine.finishReport(ctx, report), fmt.Errorf("unknown team push status %q", result.Status)
		}
	}
	return engine.finishReport(ctx, report), nil
}

func (engine *Engine) reconcilePulled(ctx context.Context, pulled []model.Event, report *SyncReport) error {
	if len(pulled) == 0 {
		return nil
	}
	acceptedByID := make(map[string]model.Event, len(pulled))
	for _, event := range pulled {
		acceptedByID[event.ID] = event
	}
	entries, err := engine.outbox.ReadAll(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Status != Pending {
			continue
		}
		matched := make([]model.Event, 0, len(entry.Batch.Events))
		for _, event := range entry.Batch.Events {
			accepted, exists := acceptedByID[event.ID]
			if !exists {
				matched = nil
				break
			}
			matched = append(matched, accepted)
		}
		if len(matched) != len(entry.Batch.Events) {
			continue
		}
		result := team.PushResult{Status: team.PushAccepted, TeamSeqFrom: matched[0].Sequence, TeamSeqTo: matched[len(matched)-1].Sequence, Events: matched}
		if err := engine.outbox.Update(ctx, entry.Batch.BatchID, result); err != nil {
			return fmt.Errorf("persist pulled reconciliation: %w", err)
		}
		report.Results = append(report.Results, result)
		report.Accepted++
	}
	return nil
}

func (engine *Engine) preflightPending(ctx context.Context, report *SyncReport) ([]OutboxEntry, error) {
	history, err := engine.accepted.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := engine.outbox.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	working := append([]model.Event(nil), history...)
	pending := make([]OutboxEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Status != Pending {
			continue
		}
		candidate := append(append([]model.Event(nil), working...), entry.Batch.Events...)
		if _, err := model.Reduce(candidate); err != nil {
			result := team.PushResult{Status: team.PushRejected, Code: invalidLocalTransitionCode, Message: err.Error()}
			if err := engine.outbox.Update(ctx, entry.Batch.BatchID, result); err != nil {
				return nil, fmt.Errorf("persist invalid local batch: %w", err)
			}
			report.Results = append(report.Results, result)
			report.Rejected++
			continue
		}
		working = candidate
		pending = append(pending, entry)
	}
	return pending, nil
}

func (engine *Engine) finishReport(ctx context.Context, report SyncReport) SyncReport {
	entries, err := engine.outbox.ReadAll(ctx)
	if err != nil {
		return report
	}
	report.Pending = 0
	for _, entry := range entries {
		if entry.Status == Pending {
			report.Pending++
		}
	}
	report.Cursor = engine.config.Cursor
	return report
}

func remoteError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %v", ErrOffline, err)
}

package eventstore

import (
	"context"

	"github.com/haochase/haowork/internal/model"
)

// BatchSink exposes Store's expected-count guarded batch append to transfer writers.
type BatchSink struct{ Store Store }

func NewBatchSink(store Store) BatchSink { return BatchSink{Store: store} }

func (sink BatchSink) AppendBatch(ctx context.Context, events []model.Event) error {
	history, err := sink.Store.ReadAll(ctx)
	if err != nil {
		return err
	}
	_, err = sink.Store.AppendBatchIfUnchanged(ctx, events, len(history))
	return err
}

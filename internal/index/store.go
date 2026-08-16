package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/haochase/haowork/internal/model"
)

var (
	ErrHistoryNotIndexed   = errors.New("history is not indexed")
	ErrWatermarkNotIndexed = errors.New("index watermark is not available")
	ErrStoreClosed         = errors.New("local index is closed")
)

// Watermark identifies the JSONL snapshot represented by a derived index.
type Watermark struct {
	EventCount     uint64
	ChangeCount    uint64
	LatestSequence uint64
}

func (w Watermark) Equal(other Watermark) bool {
	return w == other
}

func (w Watermark) IsOlderThan(other Watermark) bool {
	if w.LatestSequence != other.LatestSequence {
		return w.LatestSequence < other.LatestSequence
	}
	if w.EventCount != other.EventCount {
		return w.EventCount < other.EventCount
	}
	return w.ChangeCount < other.ChangeCount
}

func WatermarkForEvents(events []model.Event) (Watermark, error) {
	watermark := Watermark{EventCount: uint64(len(events))}
	for _, event := range events {
		if event.Sequence > watermark.LatestSequence {
			watermark.LatestSequence = event.Sequence
		}
		switch event.Type {
		case "changes.scanned":
			var payload model.ChangesScanned
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Watermark{}, fmt.Errorf("decode scanned changes event %q for index watermark: %w", event.ID, err)
			}
			watermark.ChangeCount += uint64(len(payload.Changes))
		case "change.attributed":
			var payload model.ChangeAttributed
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return Watermark{}, fmt.Errorf("decode attributed change event %q for index watermark: %w", event.ID, err)
			}
			watermark.ChangeCount++
		}
	}
	return watermark, nil
}

// Store is a discardable local query index derived from the event history.
type Store interface {
	Rebuild(context.Context, []model.Event) error
	Watermark(context.Context) (Watermark, error)
	SearchHistory(context.Context, string, int) ([]model.Event, error)
	Close() error
}

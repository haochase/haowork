package index

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/haochase/haowork/internal/model"
	_ "modernc.org/sqlite"
)

type sqliteStore struct {
	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

func Open(root string) (Store, error) {
	directory := filepath.Join(root, ".haowork", "index")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create local index directory: %w", err)
	}
	databasePath := filepath.Join(directory, "local.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open local index: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &sqliteStore{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *sqliteStore) Rebuild(ctx context.Context, events []model.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	watermark, err := WatermarkForEvents(events)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin local index rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	indexed, available, err := readWatermark(ctx, tx)
	if err != nil {
		return err
	}
	if available && watermark.IsOlderThan(indexed) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM change_index"); err != nil {
		return fmt.Errorf("clear change index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM event_index"); err != nil {
		return fmt.Errorf("clear event index: %w", err)
	}

	eventStatement, err := tx.PrepareContext(ctx, `
		INSERT INTO event_index (sequence, event_id, aggregate_id, event_json)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare event index insert: %w", err)
	}
	defer eventStatement.Close()
	changeStatement, err := tx.PrepareContext(ctx, `
		INSERT INTO change_index (
			event_sequence, event_id, event_type, path, sha256, status, baseline, attributed, task_id, note
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare change index insert: %w", err)
	}
	defer changeStatement.Close()

	for _, event := range events {
		if event.Sequence > math.MaxInt64 {
			return fmt.Errorf("event sequence %d exceeds SQLite integer range", event.Sequence)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encode event %q for local index: %w", event.ID, err)
		}
		if _, err := eventStatement.ExecContext(ctx, int64(event.Sequence), event.ID, event.AggregateID, encoded); err != nil {
			return fmt.Errorf("index event %q: %w", event.ID, err)
		}
		if err := indexChanges(ctx, changeStatement, event); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM index_watermark"); err != nil {
		return fmt.Errorf("clear index watermark: %w", err)
	}
	if err := writeWatermark(ctx, tx, watermark); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit local index rebuild: %w", err)
	}
	return nil
}

func (s *sqliteStore) Watermark(ctx context.Context) (Watermark, error) {
	if err := ctx.Err(); err != nil {
		return Watermark{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.database()
	if err != nil {
		return Watermark{}, err
	}
	watermark, available, err := readWatermark(ctx, db)
	if err != nil {
		return Watermark{}, err
	}
	if !available {
		return Watermark{}, ErrWatermarkNotIndexed
	}
	return watermark, nil
}

func (s *sqliteStore) SearchHistory(ctx context.Context, aggregateID string, limit int) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.database()
	if err != nil {
		return nil, err
	}
	query := "SELECT event_json FROM event_index"
	args := make([]any, 0, 2)
	if aggregateID != "" {
		query += " WHERE aggregate_id = ?"
		args = append(args, aggregateID)
	}
	query += " ORDER BY sequence ASC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search local history index: %w", err)
	}
	defer rows.Close()

	events := make([]model.Event, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("read local history index: %w", err)
		}
		var event model.Event
		if err := json.Unmarshal(encoded, &event); err != nil {
			return nil, fmt.Errorf("decode local history index: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local history index: %w", err)
	}
	if len(events) == 0 {
		return nil, ErrHistoryNotIndexed
	}
	return events, nil
}

func (s *sqliteStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	db := s.db
	s.db = nil
	s.mu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

func (s *sqliteStore) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS event_index (
			sequence INTEGER PRIMARY KEY,
			event_id TEXT NOT NULL,
			aggregate_id TEXT NOT NULL,
			event_json BLOB NOT NULL
		);
		CREATE INDEX IF NOT EXISTS event_index_aggregate_sequence
			ON event_index (aggregate_id, sequence);
		CREATE TABLE IF NOT EXISTS change_index (
			event_sequence INTEGER NOT NULL,
			event_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			path TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			status TEXT NOT NULL,
			baseline TEXT NOT NULL,
			attributed INTEGER NOT NULL,
			task_id TEXT NOT NULL,
			note TEXT NOT NULL,
			PRIMARY KEY (event_sequence, path)
		);
		CREATE TABLE IF NOT EXISTS index_watermark (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			event_count INTEGER NOT NULL,
			change_count INTEGER NOT NULL,
			latest_sequence INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("initialize local index: %w", err)
	}
	return nil
}

func (s *sqliteStore) database() (*sql.DB, error) {
	if s.closed || s.db == nil {
		return nil, ErrStoreClosed
	}
	return s.db, nil
}

type watermarkReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readWatermark(ctx context.Context, reader watermarkReader) (Watermark, bool, error) {
	var eventCount, changeCount, latestSequence int64
	err := reader.QueryRowContext(ctx, `
		SELECT event_count, change_count, latest_sequence
		FROM index_watermark
		WHERE singleton = 1`).Scan(&eventCount, &changeCount, &latestSequence)
	if err == nil {
		watermark, conversionErr := watermarkFromIntegers(eventCount, changeCount, latestSequence)
		if conversionErr != nil {
			return Watermark{}, false, conversionErr
		}
		return watermark, true, nil
	}
	if err == sql.ErrNoRows {
		return Watermark{}, false, nil
	}
	return Watermark{}, false, fmt.Errorf("read local index watermark: %w", err)
}

func writeWatermark(ctx context.Context, tx *sql.Tx, watermark Watermark) error {
	eventCount, changeCount, latestSequence, err := watermarkIntegers(watermark)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO index_watermark (singleton, event_count, change_count, latest_sequence)
		VALUES (1, ?, ?, ?)`, eventCount, changeCount, latestSequence); err != nil {
		return fmt.Errorf("write local index watermark: %w", err)
	}
	return nil
}

func watermarkIntegers(watermark Watermark) (int64, int64, int64, error) {
	if watermark.EventCount > math.MaxInt64 || watermark.ChangeCount > math.MaxInt64 || watermark.LatestSequence > math.MaxInt64 {
		return 0, 0, 0, fmt.Errorf("local index watermark exceeds SQLite integer range")
	}
	return int64(watermark.EventCount), int64(watermark.ChangeCount), int64(watermark.LatestSequence), nil
}

func watermarkFromIntegers(eventCount, changeCount, latestSequence int64) (Watermark, error) {
	if eventCount < 0 || changeCount < 0 || latestSequence < 0 {
		return Watermark{}, fmt.Errorf("local index watermark contains a negative value")
	}
	return Watermark{
		EventCount:     uint64(eventCount),
		ChangeCount:    uint64(changeCount),
		LatestSequence: uint64(latestSequence),
	}, nil
}

func indexChanges(ctx context.Context, statement *sql.Stmt, event model.Event) error {
	sequence := int64(event.Sequence)
	switch event.Type {
	case "changes.scanned":
		var payload model.ChangesScanned
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode scanned changes event %q: %w", event.ID, err)
		}
		for _, change := range payload.Changes {
			if _, err := statement.ExecContext(ctx, sequence, event.ID, event.Type, change.Path, change.SHA256, change.Status, change.Baseline, boolToInteger(change.Attributed), "", ""); err != nil {
				return fmt.Errorf("index scanned change %q: %w", change.Path, err)
			}
		}
	case "change.attributed":
		var payload model.ChangeAttributed
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode attributed change event %q: %w", event.ID, err)
		}
		if _, err := statement.ExecContext(ctx, sequence, event.ID, event.Type, payload.Path, payload.SHA256, "", "", 1, payload.TaskID, payload.Note); err != nil {
			return fmt.Errorf("index attributed change %q: %w", payload.Path, err)
		}
	}
	return nil
}

func boolToInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

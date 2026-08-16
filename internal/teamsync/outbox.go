package teamsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/team"
)

const maxOutboxLineBytes = 16 * 1024 * 1024

type EntryStatus string

const (
	Pending   EntryStatus = "Pending"
	Accepted  EntryStatus = "Accepted"
	Rejected  EntryStatus = "Rejected"
	Conflict  EntryStatus = "Conflict"
	Withdrawn EntryStatus = "Withdrawn"
)

type OutboxEntry struct {
	Batch        team.PushBatch  `json:"batch"`
	Status       EntryStatus     `json:"status"`
	Result       team.PushResult `json:"result,omitempty"`
	Materialized bool            `json:"materialized"`
	GitCommitted bool            `json:"git_committed"`
	UpdatedAt    time.Time       `json:"updated_at"`
	order        uint64
}

type Outbox struct {
	path     string
	lockPath string
}

func NewOutbox(root, deviceID string) Outbox {
	directory := filepath.Join(root, ".haowork", "local", deviceID)
	return Outbox{path: filepath.Join(directory, "outbox.jsonl"), lockPath: deviceLockPath(root, deviceID)}
}

func (outbox Outbox) Append(ctx context.Context, batch team.PushBatch) error {
	if batch.BatchID == "" || len(batch.Events) == 0 {
		return errors.New("outbox batch id and events are required")
	}
	return outbox.withLock(ctx, func() error {
		entries, err := outbox.readUnlocked()
		if err != nil {
			return err
		}
		if _, exists := entries[batch.BatchID]; exists {
			return fmt.Errorf("outbox batch %q already exists", batch.BatchID)
		}
		return outbox.appendUnlocked(OutboxEntry{Batch: batch, Status: Pending, UpdatedAt: time.Now().UTC()})
	})
}

func (outbox Outbox) Update(ctx context.Context, batchID string, result team.PushResult) error {
	return outbox.withLock(ctx, func() error {
		entries, err := outbox.readUnlocked()
		if err != nil {
			return err
		}
		entry, exists := entries[batchID]
		if !exists {
			return fmt.Errorf("outbox batch %q not found", batchID)
		}
		status, err := statusForResult(result)
		if err != nil {
			return err
		}
		if entry.Status != Pending {
			if entry.Status == status && reflect.DeepEqual(entry.Result, result) {
				return nil
			}
			return fmt.Errorf("outbox batch %q already has terminal status %q", batchID, entry.Status)
		}
		entry.Status, entry.Result, entry.Materialized, entry.UpdatedAt = status, result, result.Materialized, time.Now().UTC()
		return outbox.appendUnlocked(entry)
	})
}

// RecordRetryableResult preserves the most recent stale-baseline response
// without changing the batch's Pending status. A later Sync must pull/rebase
// and may safely send the original batch again.
func (outbox Outbox) RecordRetryableResult(ctx context.Context, batchID string, result team.PushResult) error {
	if result.Status != team.PushConflict || result.Code != team.CodeStaleBaseline {
		return errors.New("retryable outbox result must be a stale baseline conflict")
	}
	return outbox.withLock(ctx, func() error {
		entries, err := outbox.readUnlocked()
		if err != nil {
			return err
		}
		entry, exists := entries[batchID]
		if !exists {
			return fmt.Errorf("outbox batch %q not found", batchID)
		}
		if entry.Status != Pending {
			return fmt.Errorf("outbox batch %q is not pending", batchID)
		}
		if reflect.DeepEqual(entry.Result, result) {
			return nil
		}
		entry.Result, entry.UpdatedAt = result, time.Now().UTC()
		return outbox.appendUnlocked(entry)
	})
}

func (outbox Outbox) ReadAll(ctx context.Context) ([]OutboxEntry, error) {
	var result []OutboxEntry
	err := outbox.withReadLock(ctx, func() error {
		entries, err := outbox.readUnlocked()
		if err != nil {
			return err
		}
		result = make([]OutboxEntry, 0, len(entries))
		for _, entry := range entries {
			result = append(result, entry)
		}
		sort.Slice(result, func(left, right int) bool { return result[left].order < result[right].order })
		return nil
	})
	return result, err
}

func statusForResult(result team.PushResult) (EntryStatus, error) {
	switch result.Status {
	case team.PushAccepted:
		return Accepted, nil
	case team.PushRejected:
		return Rejected, nil
	case team.PushConflict:
		return Conflict, nil
	default:
		return "", fmt.Errorf("unknown outbox result status %q", result.Status)
	}
}

func (outbox Outbox) withLock(ctx context.Context, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outbox.path), 0o755); err != nil {
		return err
	}
	lock := flock.New(outbox.lockPath)
	locked, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("outbox is busy")
	}
	defer lock.Unlock()
	return action()
}

func (outbox Outbox) withReadLock(ctx context.Context, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outbox.path), 0o755); err != nil {
		return err
	}
	lock := flock.New(outbox.lockPath)
	locked, err := lock.TryRLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("outbox is busy")
	}
	defer lock.Unlock()
	return action()
}

func (outbox Outbox) readUnlocked() (map[string]OutboxEntry, error) {
	file, err := os.Open(outbox.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]OutboxEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make(map[string]OutboxEntry)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxOutboxLineBytes)
	var lineNumber uint64
	for scanner.Scan() {
		lineNumber++
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var entry OutboxEntry
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("decode outbox record: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, errors.New("outbox record has trailing content")
		}
		if entry.Batch.BatchID == "" {
			return nil, errors.New("outbox record batch id is required")
		}
		entry.order = lineNumber
		entries[entry.Batch.BatchID] = entry
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (outbox Outbox) appendUnlocked(entry OutboxEntry) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(encoded) >= maxOutboxLineBytes {
		return errors.New("outbox record exceeds 16 MiB")
	}
	original, err := os.ReadFile(outbox.path)
	if errors.Is(err, os.ErrNotExist) {
		original = nil
	} else if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(outbox.path), "outbox-*.jsonl")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(original); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, outbox.path)
}

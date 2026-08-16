package eventstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/model"
)

var (
	ErrHistoryCorrupt            = errors.New("event history is corrupt")
	ErrStoreBusy                 = errors.New("event store is busy")
	ErrEventTooLarge             = errors.New("event exceeds 16 MiB scanner limit")
	ErrStateChanged              = errors.New("event store state changed")
	ErrDuplicateDivergence       = errors.New("event duplicate diverges from stored history")
	ErrAcceptedHistoryDivergence = errors.New("accepted event history diverges")
)

const maxEventLineBytes = 16 * 1024 * 1024

type Store struct {
	path     string
	lockPath string
}

func New(projectRoot string) Store {
	return NewAt(
		filepath.Join(projectRoot, ".haowork", "events.jsonl"),
		filepath.Join(projectRoot, ".haowork", "runtime", "events.lock"),
	)
}

func NewAt(path, lockPath string) Store {
	return Store{path: path, lockPath: lockPath}
}

func (s Store) ensureRuntimeDir() error {
	return os.MkdirAll(filepath.Dir(s.lockPath), 0o755)
}

func (s Store) Append(ctx context.Context, candidate model.Event) (model.Event, error) {
	if err := ctx.Err(); err != nil {
		return model.Event{}, err
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return model.Event{}, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Event{}, ErrStoreBusy
	}
	if err != nil {
		return model.Event{}, err
	}
	if !locked {
		return model.Event{}, ErrStoreBusy
	}
	defer fileLock.Unlock()

	existing, err := s.readAllUnlocked()
	if err != nil {
		return model.Event{}, err
	}
	return s.appendWithExisting(candidate, existing)
}

// AppendIfUnchanged appends only when the event count observed by the caller
// is still current. The precondition check and append share the store's writer
// lock, preventing stale projection checks from creating illegal transitions.
func (s Store) AppendIfUnchanged(ctx context.Context, candidate model.Event, expectedEventCount int) (model.Event, error) {
	if err := ctx.Err(); err != nil {
		return model.Event{}, err
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return model.Event{}, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return model.Event{}, ErrStoreBusy
	}
	if err != nil {
		return model.Event{}, err
	}
	if !locked {
		return model.Event{}, ErrStoreBusy
	}
	defer fileLock.Unlock()

	existing, err := s.readAllUnlocked()
	if err != nil {
		return model.Event{}, err
	}
	if len(existing) != expectedEventCount {
		return model.Event{}, ErrStateChanged
	}
	return s.appendWithExisting(candidate, existing)
}

// AppendBatchIfUnchanged appends a complete transition only when the caller's
// snapshot is current. It writes a replacement JSONL file so a failed batch
// cannot expose a prefix of the transition to replay.
func (s Store) AppendBatchIfUnchanged(ctx context.Context, candidates []model.Event, expectedEventCount int) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("event batch is required")
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return nil, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrStoreBusy
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrStoreBusy
	}
	defer fileLock.Unlock()

	existing, err := s.readAllUnlocked()
	if err != nil {
		return nil, err
	}
	if len(existing) != expectedEventCount {
		return nil, ErrStateChanged
	}
	return s.appendBatchWithExisting(candidates, existing)
}

// AppendBatchIdempotent appends a complete team transition once. A retry of
// the complete stored batch returns its original chain fields, while partial
// or divergent retries are rejected under the same writer lock.
func (s Store) AppendBatchIdempotent(ctx context.Context, candidates []model.Event, expectedCount int) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("event batch is required")
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return nil, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrStoreBusy
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrStoreBusy
	}
	defer fileLock.Unlock()

	existing, err := s.readAllUnlocked()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]model.Event, len(existing))
	for _, event := range existing {
		byID[event.ID] = event
	}

	stored := make([]model.Event, 0, len(candidates))
	seenCandidates := make(map[string]struct{}, len(candidates))
	found := 0
	for _, candidate := range candidates {
		if _, seen := seenCandidates[candidate.ID]; seen {
			return nil, fmt.Errorf("%w: event id %q occurs more than once", ErrDuplicateDivergence, candidate.ID)
		}
		seenCandidates[candidate.ID] = struct{}{}
		event, exists := byID[candidate.ID]
		if !exists {
			continue
		}
		found++
		if !equalNonChainEvent(event, candidate) {
			return nil, fmt.Errorf("%w: event id %q has different content", ErrDuplicateDivergence, candidate.ID)
		}
		stored = append(stored, event)
	}
	if found == len(candidates) {
		return stored, nil
	}
	if found > 0 {
		return nil, fmt.Errorf("%w: retry contains an already stored prefix", ErrDuplicateDivergence)
	}
	if len(existing) != expectedCount {
		return nil, ErrStateChanged
	}
	return s.appendBatchWithExisting(candidates, existing)
}

// ImportAcceptedBatch imports the exact event chain accepted by the team. It
// verifies remote chain fields before replacing the JSONL file and never
// recalculates accepted hashes.
func (s Store) ImportAcceptedBatch(ctx context.Context, accepted []model.Event) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(accepted) == 0 {
		return nil, errors.New("accepted event batch is required")
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return nil, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrStoreBusy
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrStoreBusy
	}
	defer fileLock.Unlock()

	existing, err := s.readAllUnlocked()
	if err != nil {
		return nil, err
	}
	knownIDs := make(map[string]struct{}, len(existing)+len(accepted))
	for _, event := range existing {
		knownIDs[event.ID] = struct{}{}
	}
	working := append([]model.Event(nil), existing...)
	stored := make([]model.Event, 0, len(accepted))
	newEvents := make([]model.Event, 0, len(accepted))
	for index, candidate := range accepted {
		if candidate.ID == "" {
			return nil, fmt.Errorf("%w: event id is required", ErrAcceptedHistoryDivergence)
		}
		if index > 0 && candidate.Sequence != accepted[index-1].Sequence+1 {
			return nil, fmt.Errorf("%w: sequence %d does not follow %d", ErrAcceptedHistoryDivergence, candidate.Sequence, accepted[index-1].Sequence)
		}
		if candidate.Sequence == 0 || candidate.Sequence > uint64(len(working)+1) {
			return nil, fmt.Errorf("%w: unexpected sequence %d", ErrAcceptedHistoryDivergence, candidate.Sequence)
		}
		if candidate.Sequence <= uint64(len(working)) {
			event := working[candidate.Sequence-1]
			if !equalEvent(event, candidate) {
				if event.ID == candidate.ID {
					return nil, fmt.Errorf("%w: %w: event id %q has different content", ErrAcceptedHistoryDivergence, ErrDuplicateDivergence, candidate.ID)
				}
				return nil, fmt.Errorf("%w: sequence %d differs from stored event", ErrAcceptedHistoryDivergence, candidate.Sequence)
			}
			stored = append(stored, event)
			continue
		}
		if _, exists := knownIDs[candidate.ID]; exists {
			return nil, fmt.Errorf("%w: %w: event id %q already exists", ErrAcceptedHistoryDivergence, ErrDuplicateDivergence, candidate.ID)
		}
		if err := verifyEvent(candidate, working); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAcceptedHistoryDivergence, err)
		}
		line, err := json.Marshal(candidate)
		if err != nil {
			return nil, err
		}
		if len(line) >= maxEventLineBytes {
			return nil, ErrEventTooLarge
		}
		knownIDs[candidate.ID] = struct{}{}
		working = append(working, candidate)
		newEvents = append(newEvents, candidate)
		stored = append(stored, candidate)
	}
	if len(newEvents) == 0 {
		return stored, nil
	}
	if err := s.replaceWithEvents(newEvents); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s Store) appendWithExisting(candidate model.Event, existing []model.Event) (model.Event, error) {
	var err error
	for _, event := range existing {
		if event.ID != candidate.ID {
			continue
		}
		candidate.Sequence = event.Sequence
		candidate.PreviousHash = event.PreviousHash
		candidate.Hash = event.Hash
		if equalEvent(event, candidate) {
			return event, nil
		}
		return model.Event{}, fmt.Errorf("event id %s already exists with different content", candidate.ID)
	}
	candidate.Sequence = uint64(len(existing) + 1)
	candidate.PreviousHash = ""
	if len(existing) > 0 {
		candidate.PreviousHash = existing[len(existing)-1].Hash
	}
	candidate.Hash, err = hashEvent(candidate)
	if err != nil {
		return model.Event{}, err
	}
	line, err := json.Marshal(candidate)
	if err != nil {
		return model.Event{}, err
	}
	if len(line) >= maxEventLineBytes {
		return model.Event{}, ErrEventTooLarge
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return model.Event{}, err
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return model.Event{}, err
	}
	if err := file.Sync(); err != nil {
		return model.Event{}, err
	}
	return candidate, nil
}

func (s Store) appendBatchWithExisting(candidates, existing []model.Event) ([]model.Event, error) {
	prepared := make([]model.Event, 0, len(candidates))
	knownIDs := make(map[string]struct{}, len(existing)+len(candidates))
	for _, event := range existing {
		knownIDs[event.ID] = struct{}{}
	}
	preceding := append([]model.Event(nil), existing...)
	for _, candidate := range candidates {
		if _, exists := knownIDs[candidate.ID]; exists {
			return nil, fmt.Errorf("event id %s already exists", candidate.ID)
		}
		if candidate.ID == "" {
			return nil, errors.New("event id is required")
		}
		knownIDs[candidate.ID] = struct{}{}
		candidate.Sequence = uint64(len(preceding) + 1)
		candidate.PreviousHash = ""
		if len(preceding) > 0 {
			candidate.PreviousHash = preceding[len(preceding)-1].Hash
		}
		var err error
		candidate.Hash, err = hashEvent(candidate)
		if err != nil {
			return nil, err
		}
		line, err := json.Marshal(candidate)
		if err != nil {
			return nil, err
		}
		if len(line) >= maxEventLineBytes {
			return nil, ErrEventTooLarge
		}
		prepared = append(prepared, candidate)
		preceding = append(preceding, candidate)
	}

	original, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, "events-batch-*.jsonl")
	if err != nil {
		return nil, err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(original); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	for _, event := range prepared {
		line, err := json.Marshal(event)
		if err != nil {
			_ = temporary.Close()
			return nil, err
		}
		if _, err := temporary.Write(append(line, '\n')); err != nil {
			_ = temporary.Close()
			return nil, err
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return nil, err
	}
	return prepared, nil
}

func (s Store) replaceWithEvents(events []model.Event) error {
	original, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, "events-import-*.jsonl")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := temporary.Write(original); err != nil {
		_ = temporary.Close()
		return err
	}
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			_ = temporary.Close()
			return err
		}
		if _, err := temporary.Write(append(line, '\n')); err != nil {
			_ = temporary.Close()
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, s.path)
}

func (s Store) ReadAll(ctx context.Context) ([]model.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.ensureRuntimeDir(); err != nil {
		return nil, err
	}
	fileLock := flock.New(s.lockPath)
	locked, err := fileLock.TryRLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrStoreBusy
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrStoreBusy
	}
	defer fileLock.Unlock()
	return s.readAllUnlocked()
}

func (s Store) readAllUnlocked() ([]model.Event, error) {
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []model.Event
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxEventLineBytes)
	for scanner.Scan() {
		var event model.Event
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("%w: decode sequence %d: %v", ErrHistoryCorrupt, len(events)+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, fmt.Errorf("%w: multiple values at sequence %d", ErrHistoryCorrupt, len(events)+1)
			}
			return nil, fmt.Errorf("%w: trailing content at sequence %d: %v", ErrHistoryCorrupt, len(events)+1, err)
		}
		if err := verifyEvent(event, events); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func hashEvent(event model.Event) (string, error) {
	event.Hash = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func verifyEvent(event model.Event, preceding []model.Event) error {
	wantSequence := uint64(len(preceding) + 1)
	if event.Sequence != wantSequence {
		return fmt.Errorf("%w: sequence %d, want %d", ErrHistoryCorrupt, event.Sequence, wantSequence)
	}
	wantPrevious := ""
	if len(preceding) > 0 {
		wantPrevious = preceding[len(preceding)-1].Hash
	}
	if event.PreviousHash != wantPrevious {
		return fmt.Errorf("%w: previous hash mismatch at %d", ErrHistoryCorrupt, event.Sequence)
	}
	wantHash, err := hashEvent(event)
	if err != nil {
		return err
	}
	if event.Hash != wantHash {
		return fmt.Errorf("%w: hash mismatch at %d", ErrHistoryCorrupt, event.Sequence)
	}
	return nil
}

func equalEvent(a, b model.Event) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func equalNonChainEvent(a, b model.Event) bool {
	a.Sequence = 0
	a.PreviousHash = ""
	a.Hash = ""
	b.Sequence = 0
	b.PreviousHash = ""
	b.Hash = ""
	return equalEvent(a, b)
}

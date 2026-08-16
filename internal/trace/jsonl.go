package trace

import (
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
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/model"
)

const maxTraceLineBytes = 16 * 1024 * 1024

type jsonlStore struct {
	path     string
	lockPath string
}

func New(projectRoot string) Store {
	return NewAt(filepath.Join(projectRoot, ".haowork", "trace", "execution.jsonl"), filepath.Join(projectRoot, ".haowork", "trace", "execution.lock"))
}

func NewAt(path, lockPath string) Store {
	return jsonlStore{path: path, lockPath: lockPath}
}

func (store jsonlStore) AppendIdempotent(ctx context.Context, candidate Envelope) (Envelope, error) {
	stored, _, err := store.append(ctx, candidate, false, false)
	return stored, err
}

// AppendInvocationRoot atomically reuses an invocation's established root timestamp.
func (store jsonlStore) AppendInvocationRoot(ctx context.Context, candidate Envelope) (Envelope, error) {
	stored, _, err := store.append(ctx, candidate, false, true)
	return stored, err
}

// AppendMatrixIdempotent validates an opaque Matrix cursor's separate local observation sequence under the writer lock.
func (store jsonlStore) AppendMatrixIdempotent(ctx context.Context, candidate Envelope) (Envelope, error) {
	stored, _, err := store.append(ctx, candidate, true, false)
	return stored, err
}

// AppendIdempotentResult exposes whether a candidate was appended for internal idempotent deliveries.
func (store jsonlStore) AppendIdempotentResult(ctx context.Context, candidate Envelope) (Envelope, bool, error) {
	return store.append(ctx, candidate, false, false)
}

func (store jsonlStore) append(ctx context.Context, candidate Envelope, matrix, invocationRoot bool) (Envelope, bool, error) {
	if err := ctx.Err(); err != nil {
		return Envelope{}, false, err
	}
	if err := store.ensurePaths(); err != nil {
		return Envelope{}, false, err
	}
	lock := flock.New(store.lockPath)
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) || !locked {
		return Envelope{}, false, ErrStoreBusy
	}
	if err != nil {
		return Envelope{}, false, err
	}
	defer lock.Unlock()

	existing, truncated, err := store.readAllUnlocked()
	if err != nil {
		return Envelope{}, false, err
	}
	if truncated {
		if err := store.removeTruncatedTail(); err != nil {
			return Envelope{}, false, err
		}
	}
	candidate, err = canonicalEnvelope(candidate)
	if err != nil {
		return Envelope{}, false, err
	}
	key := externalKey(candidate)
	for _, record := range existing {
		if externalKey(record) == key {
			stored, err := canonicalEnvelope(record)
			if err != nil {
				return Envelope{}, false, fmt.Errorf("%w: invalid stored record", ErrHistoryCorrupt)
			}
			if equalCanonicalEnvelope(stored, candidate) || (invocationRoot && equalInvocationRoot(stored, candidate)) {
				return cloneEnvelope(record), false, nil
			}
			return Envelope{}, false, ErrSourceDivergent
		}
		if record.ID != candidate.ID {
			continue
		}
		stored, err := canonicalEnvelope(record)
		if err != nil {
			return Envelope{}, false, fmt.Errorf("%w: invalid stored record", ErrHistoryCorrupt)
		}
		if equalCanonicalEnvelope(stored, candidate) {
			return cloneEnvelope(record), false, nil
		}
		return Envelope{}, false, ErrTraceIDDivergent
	}
	if matrix {
		if err := validateMatrixObservation(existing, candidate); err != nil {
			return Envelope{}, false, err
		}
	}
	if _, err := Replay(append(append([]Envelope(nil), existing...), candidate)); err != nil {
		return Envelope{}, false, err
	}
	candidate.Sequence = uint64(len(existing) + 1)
	if len(existing) > 0 {
		candidate.PreviousHash = existing[len(existing)-1].Hash
	}
	candidate.Hash, err = hashEnvelope(candidate)
	if err != nil {
		return Envelope{}, false, err
	}
	line, err := json.Marshal(candidate)
	if err != nil {
		return Envelope{}, false, err
	}
	if len(line) >= maxTraceLineBytes {
		return Envelope{}, false, fmt.Errorf("%w: trace exceeds 16 MiB", ErrHistoryCorrupt)
	}
	file, err := os.OpenFile(store.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Envelope{}, false, err
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		return Envelope{}, false, err
	}
	if err := file.Sync(); err != nil {
		return Envelope{}, false, err
	}
	return cloneEnvelope(candidate), true, nil
}

func equalInvocationRoot(left, right Envelope) bool {
	if left.ParentTraceID != "" || right.ParentTraceID != "" {
		return false
	}
	if left.ID == "" || right.ID == "" || left.ID != right.ID || left.SourceEventType != "trace.invocation.started" || right.SourceEventType != "trace.invocation.started" || left.Status != "started" || right.Status != "started" {
		return false
	}
	left.StartedAt, left.FinishedAt = time.Time{}, time.Time{}
	right.StartedAt, right.FinishedAt = time.Time{}, time.Time{}
	return equalCanonicalEnvelope(left, right)
}

func validateMatrixObservation(existing []Envelope, candidate Envelope) error {
	if candidate.SourceSystem != "matrix" || candidate.ObservationSequence == 0 {
		return ErrCursorRollback
	}
	var last uint64
	for _, record := range existing {
		if record.SourceSystem != "matrix" || record.AgentTeamsInstanceID != candidate.AgentTeamsInstanceID || record.RoomID != candidate.RoomID {
			continue
		}
		if record.ObservationSequence == candidate.ObservationSequence && record.SourceEventID != candidate.SourceEventID {
			return ErrSourceDivergent
		}
		if record.ObservationSequence > last {
			last = record.ObservationSequence
		}
	}
	if last == 0 && candidate.ObservationSequence != 1 {
		return ErrCursorGap
	}
	if last > 0 && candidate.ObservationSequence < last {
		return ErrCursorRollback
	}
	if last > 0 && candidate.ObservationSequence > last+1 {
		return ErrCursorGap
	}
	return nil
}

func (store jsonlStore) ReadAll(ctx context.Context) ([]Envelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.ensurePaths(); err != nil {
		return nil, err
	}
	lock := flock.New(store.lockPath)
	locked, err := lock.TryRLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) || !locked {
		return nil, ErrStoreBusy
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	records, _, err := store.readAllUnlocked()
	return records, err
}

func (store jsonlStore) Since(ctx context.Context, sequence uint64) ([]Envelope, error) {
	records, err := store.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Envelope, 0, len(records))
	for _, record := range records {
		if record.Sequence > sequence {
			result = append(result, cloneEnvelope(record))
		}
	}
	return result, nil
}

func (store jsonlStore) ensurePaths() error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(store.lockPath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(store.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func (store jsonlStore) readAllUnlocked() ([]Envelope, bool, error) {
	contents, err := os.ReadFile(store.path)
	if err != nil {
		return nil, false, err
	}
	if len(contents) == 0 {
		return nil, false, nil
	}
	lines := bytes.Split(contents, []byte{'\n'})
	truncated := len(lines) > 0 && len(lines[len(lines)-1]) > 0
	if truncated {
		lines = lines[:len(lines)-1]
	}
	records := make([]Envelope, 0, len(lines))
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		if len(line) >= maxTraceLineBytes {
			return nil, false, fmt.Errorf("%w: line too large", ErrHistoryCorrupt)
		}
		var record Envelope
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, false, fmt.Errorf("%w: decode sequence %d: %v", ErrHistoryCorrupt, index+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, false, fmt.Errorf("%w: trailing content at sequence %d", ErrHistoryCorrupt, index+1)
		}
		if err := verifyEnvelope(record, records); err != nil {
			return nil, false, err
		}
		records = append(records, cloneEnvelope(record))
	}
	return records, truncated, nil
}

func (store jsonlStore) removeTruncatedTail() error {
	contents, err := os.ReadFile(store.path)
	if err != nil {
		return err
	}
	lastNewline := bytes.LastIndexByte(contents, '\n')
	if lastNewline < 0 {
		return os.WriteFile(store.path, nil, 0o600)
	}
	return os.WriteFile(store.path, contents[:lastNewline+1], 0o600)
}

func verifyEnvelope(record Envelope, preceding []Envelope) error {
	canonical, err := canonicalEnvelope(record)
	if err != nil || record.Sequence != uint64(len(preceding)+1) {
		return fmt.Errorf("%w: invalid sequence %d", ErrHistoryCorrupt, record.Sequence)
	}
	previous := ""
	if len(preceding) > 0 {
		previous = preceding[len(preceding)-1].Hash
	}
	if record.PreviousHash != previous || canonical.PreviousHash != "" {
		return fmt.Errorf("%w: invalid previous hash at sequence %d", ErrHistoryCorrupt, record.Sequence)
	}
	want, err := hashEnvelope(record)
	if err != nil || record.Hash != want {
		return fmt.Errorf("%w: invalid hash at sequence %d", ErrHistoryCorrupt, record.Sequence)
	}
	return nil
}

func canonicalEnvelope(value Envelope) (Envelope, error) {
	if value.Sequence != 0 || value.PreviousHash != "" || value.Hash != "" {
		value.Sequence, value.PreviousHash, value.Hash = 0, "", ""
	}
	value.ID = strings.TrimSpace(value.ID)
	value.InvocationID = strings.TrimSpace(value.InvocationID)
	value.MissionID = strings.TrimSpace(value.MissionID)
	value.GovernanceTaskID = strings.TrimSpace(value.GovernanceTaskID)
	value.WorkItemID = strings.TrimSpace(value.WorkItemID)
	value.RunID = strings.TrimSpace(value.RunID)
	value.LogicalActorID = strings.TrimSpace(value.LogicalActorID)
	value.EnvironmentID = strings.TrimSpace(value.EnvironmentID)
	value.AgentTeamsInstanceID = strings.TrimSpace(value.AgentTeamsInstanceID)
	value.RoomID = strings.TrimSpace(value.RoomID)
	value.SenderID = strings.TrimSpace(value.SenderID)
	value.SourceEventID = strings.TrimSpace(value.SourceEventID)
	value.SourceEventType = strings.TrimSpace(value.SourceEventType)
	value.SourceSystem = strings.TrimSpace(value.SourceSystem)
	value.ParentTraceID = strings.TrimSpace(value.ParentTraceID)
	value.Cursor = strings.TrimSpace(value.Cursor)
	value.SkillName = strings.TrimSpace(value.SkillName)
	value.SkillVersion = strings.TrimSpace(value.SkillVersion)
	value.InputSHA256 = strings.TrimSpace(value.InputSHA256)
	value.OutputSHA256 = strings.TrimSpace(value.OutputSHA256)
	value.SummarySHA256 = strings.TrimSpace(value.SummarySHA256)
	value.Status = strings.TrimSpace(value.Status)
	value.ErrorCode = strings.TrimSpace(value.ErrorCode)
	value.StartedAt = value.StartedAt.UTC()
	value.FinishedAt = value.FinishedAt.UTC()
	value.ArtifactRefs = canonicalArtifacts(value.ArtifactRefs)
	value.Artifacts = canonicalArtifactObservations(value.Artifacts)
	functionRequired := value.SourceEventType != "trace.invocation.started" && !(value.SourceEventType == "skill.policy.decided" && value.Status == "denied")
	if value.ID == "" || value.MissionID == "" || value.GovernanceTaskID == "" || value.WorkItemID == "" || value.RunID == "" || value.LogicalActorID == "" || value.RuntimeBindingRevision <= 0 || (functionRequired && value.AgentFunction == "") || value.EnvironmentID == "" || value.AgentTeamsInstanceID == "" || value.SourceEventID == "" || value.SourceEventType == "" || value.Status == "" || value.StartedAt.IsZero() {
		return Envelope{}, errors.New("trace binding, source, status, and start time are required")
	}
	return value, nil
}

func canonicalArtifactObservations(values []ArtifactObservation) []ArtifactObservation {
	result := append([]ArtifactObservation(nil), values...)
	for index := range result {
		result[index].Kind = strings.TrimSpace(result[index].Kind)
		result[index].URI = strings.TrimSpace(result[index].URI)
		result[index].SHA256 = strings.TrimSpace(result[index].SHA256)
		result[index].EnvironmentID = strings.TrimSpace(result[index].EnvironmentID)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].URI+"\x00"+result[left].SHA256+"\x00"+result[left].EnvironmentID < result[right].Kind+"\x00"+result[right].URI+"\x00"+result[right].SHA256+"\x00"+result[right].EnvironmentID
	})
	return result
}

func canonicalArtifacts(values []model.ArtifactRef) []model.ArtifactRef {
	result := append([]model.ArtifactRef(nil), values...)
	for index := range result {
		result[index].Kind = strings.TrimSpace(result[index].Kind)
		result[index].URI = strings.TrimSpace(result[index].URI)
		result[index].SHA256 = strings.TrimSpace(result[index].SHA256)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].URI+"\x00"+result[left].SHA256 < result[right].Kind+"\x00"+result[right].URI+"\x00"+result[right].SHA256
	})
	return result
}

func hashEnvelope(value Envelope) (string, error) {
	value.Hash = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func equalCanonicalEnvelope(left, right Envelope) bool {
	left.Sequence, left.PreviousHash, left.Hash = 0, "", ""
	right.Sequence, right.PreviousHash, right.Hash = 0, "", ""
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func externalKey(value Envelope) string {
	return value.AgentTeamsInstanceID + "\x00" + value.SourceEventID
}

func cloneEnvelope(value Envelope) Envelope {
	value.ArtifactRefs = append([]model.ArtifactRef(nil), value.ArtifactRefs...)
	value.Artifacts = append([]ArtifactObservation(nil), value.Artifacts...)
	return value
}

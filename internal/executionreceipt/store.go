package executionreceipt

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
)

const (
	lockRetryInterval = 20 * time.Millisecond
	waitPollInterval  = 250 * time.Millisecond
	maxEventLineBytes = 16 * 1024 * 1024
)

const (
	eventAssignmentRegistered = "assignment.registered"
	eventAssignmentDispatched = "assignment.dispatched"
	eventReceiptSubmitted     = "receipt.submitted"
)

type FileStore struct {
	root     string
	path     string
	lockPath string
}

var _ Store = (*FileStore)(nil)

type event struct {
	Sequence     uint64              `json:"sequence"`
	Kind         string              `json:"kind"`
	Assignment   ExecutionAssignment `json:"assignment"`
	Receipt      *ExecutionReceipt   `json:"receipt,omitempty"`
	PreviousHash string              `json:"previous_hash,omitempty"`
	Hash         string              `json:"hash"`
}

type replayedState struct {
	assignments map[string]ExecutionAssignment
	receipts    map[string]ExecutionReceipt
	receiptIDs  map[string]string
	events      []event
}

type anchoredFiles struct {
	projectRoot *os.File
	trustRoot   *os.File
	directory   *os.File
	events      *os.File
	lock        *os.File
}

func (files *anchoredFiles) Close() error {
	return errors.Join(files.lock.Close(), files.events.Close(), files.directory.Close(), files.trustRoot.Close(), files.projectRoot.Close())
}

func Open(projectRoot string) (*FileStore, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil || root == "" {
		return nil, ErrStoreUnavailable
	}
	return &FileStore{
		root:     root,
		path:     filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl"),
		lockPath: filepath.Join(root, ".haowork", "execution-receipts", "events.lock"),
	}, nil
}

func (store *FileStore) Register(ctx context.Context, candidate ExecutionAssignment) (ExecutionAssignment, error) {
	candidate, err := canonicalAssignment(candidate)
	if err != nil {
		return ExecutionAssignment{}, err
	}
	if candidate.State != AssignmentPrepared || candidate.MatrixDispatchEventID != "" {
		return ExecutionAssignment{}, ErrInvalidTransition
	}
	return store.withWriteLockAssignment(ctx, func(state replayedState) (ExecutionAssignment, *event, error) {
		if stored, exists := state.assignments[candidate.ID]; exists {
			if stored == candidate {
				return stored, nil, nil
			}
			return ExecutionAssignment{}, nil, ErrAssignmentConflict
		}
		if !candidate.Deadline.After(time.Now().UTC()) {
			return ExecutionAssignment{}, nil, ErrAssignmentExpired
		}
		entry := event{Kind: eventAssignmentRegistered, Assignment: candidate}
		return candidate, &entry, nil
	})
}

func (store *FileStore) MarkDispatched(ctx context.Context, assignmentID, matrixEventID string) (ExecutionAssignment, error) {
	assignmentID = strings.TrimSpace(assignmentID)
	matrixEventID = strings.TrimSpace(matrixEventID)
	if assignmentID == "" || matrixEventID == "" {
		return ExecutionAssignment{}, ErrInvalidTransition
	}
	return store.withWriteLockAssignment(ctx, func(state replayedState) (ExecutionAssignment, *event, error) {
		stored, exists := state.assignments[assignmentID]
		if !exists {
			return ExecutionAssignment{}, nil, ErrAssignmentNotFound
		}
		if stored.State == AssignmentDispatched && stored.MatrixDispatchEventID == matrixEventID {
			return stored, nil, nil
		}
		if stored.State == AssignmentDispatched {
			return ExecutionAssignment{}, nil, ErrAssignmentConflict
		}
		if terminalAssignmentState(stored.State) {
			return ExecutionAssignment{}, nil, ErrAssignmentTerminal
		}
		if stored.State != AssignmentPrepared {
			return ExecutionAssignment{}, nil, ErrInvalidTransition
		}
		if !stored.Deadline.After(time.Now().UTC()) {
			return ExecutionAssignment{}, nil, ErrAssignmentExpired
		}
		stored.State = AssignmentDispatched
		stored.MatrixDispatchEventID = matrixEventID
		entry := event{Kind: eventAssignmentDispatched, Assignment: stored}
		return stored, &entry, nil
	})
}

func (store *FileStore) Submit(ctx context.Context, candidate ExecutionReceipt) (ExecutionReceipt, error) {
	candidate, err := canonicalReceipt(candidate)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	if candidate.AssignmentID != candidate.Report.AssignmentID {
		return ExecutionReceipt{}, ErrReceiptConflict
	}
	return store.withWriteLockReceipt(ctx, func(state replayedState) (ExecutionReceipt, *event, error) {
		assignment, exists := state.assignments[candidate.AssignmentID]
		if !exists {
			return ExecutionReceipt{}, nil, ErrAssignmentNotFound
		}
		if stored, exists := state.receipts[candidate.AssignmentID]; exists {
			if stored.ReportSHA256 == candidate.ReportSHA256 && equalReports(stored.Report, candidate.Report) {
				return cloneReceipt(stored), nil, nil
			}
			return ExecutionReceipt{}, nil, ErrReceiptConflict
		}
		if existingAssignmentID, exists := state.receiptIDs[candidate.ID]; exists && existingAssignmentID != candidate.AssignmentID {
			return ExecutionReceipt{}, nil, ErrReceiptConflict
		}
		if terminalAssignmentState(assignment.State) {
			return ExecutionReceipt{}, nil, ErrAssignmentTerminal
		}
		if assignment.State != AssignmentDispatched {
			return ExecutionReceipt{}, nil, ErrInvalidTransition
		}
		if !assignment.Deadline.After(time.Now().UTC()) {
			return ExecutionReceipt{}, nil, ErrAssignmentExpired
		}
		assignment.State = AssignmentReportSubmitted
		entry := event{Kind: eventReceiptSubmitted, Assignment: assignment, Receipt: &candidate}
		return cloneReceipt(candidate), &entry, nil
	})
}

func (store *FileStore) Assignment(ctx context.Context, assignmentID string) (ExecutionAssignment, error) {
	state, err := store.read(ctx)
	if err != nil {
		return ExecutionAssignment{}, err
	}
	assignment, exists := state.assignments[strings.TrimSpace(assignmentID)]
	if !exists {
		return ExecutionAssignment{}, ErrAssignmentNotFound
	}
	return assignment, nil
}

// Receipt returns the receipt submitted for an assignment ID.
func (store *FileStore) Receipt(ctx context.Context, assignmentID string) (ExecutionReceipt, error) {
	state, err := store.read(ctx)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	receipt, exists := state.receipts[strings.TrimSpace(assignmentID)]
	if !exists {
		return ExecutionReceipt{}, ErrReceiptNotFound
	}
	return cloneReceipt(receipt), nil
}

func (store *FileStore) Wait(ctx context.Context, assignmentID string, timeout time.Duration) (ExecutionReceipt, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	assignmentID = strings.TrimSpace(assignmentID)
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for {
		state, err := store.read(ctx)
		if err != nil {
			return ExecutionReceipt{}, err
		}
		if receipt, exists := state.receipts[assignmentID]; exists {
			return cloneReceipt(receipt), nil
		}
		assignment, exists := state.assignments[assignmentID]
		if !exists {
			return ExecutionReceipt{}, ErrAssignmentNotFound
		}
		if terminalAssignmentState(assignment.State) {
			return ExecutionReceipt{}, fmt.Errorf("%w: %s", ErrAssignmentTerminal, assignment.State)
		}
		if !assignment.Deadline.After(time.Now().UTC()) {
			return ExecutionReceipt{}, ErrAssignmentExpired
		}
		select {
		case <-ctx.Done():
			return ExecutionReceipt{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (store *FileStore) withWriteLockAssignment(ctx context.Context, mutate func(replayedState) (ExecutionAssignment, *event, error)) (ExecutionAssignment, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionAssignment{}, err
	}
	files, err := store.openFiles()
	if err != nil {
		return ExecutionAssignment{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	defer files.Close()
	unlock, err := lockAnchoredFile(ctx, files.lock, false)
	if err != nil {
		return ExecutionAssignment{}, err
	}
	defer unlock()
	state, err := store.readUnlocked(files.events)
	if err != nil {
		return ExecutionAssignment{}, err
	}
	result, entry, err := mutate(state)
	if err != nil || entry == nil {
		return result, err
	}
	if err := store.appendUnlocked(files.events, state.events, *entry); err != nil {
		return ExecutionAssignment{}, err
	}
	return result, nil
}

func (store *FileStore) withWriteLockReceipt(ctx context.Context, mutate func(replayedState) (ExecutionReceipt, *event, error)) (ExecutionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionReceipt{}, err
	}
	files, err := store.openFiles()
	if err != nil {
		return ExecutionReceipt{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	defer files.Close()
	unlock, err := lockAnchoredFile(ctx, files.lock, false)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	defer unlock()
	state, err := store.readUnlocked(files.events)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	result, entry, err := mutate(state)
	if err != nil || entry == nil {
		return result, err
	}
	if err := store.appendUnlocked(files.events, state.events, *entry); err != nil {
		return ExecutionReceipt{}, err
	}
	return result, nil
}

func (store *FileStore) read(ctx context.Context) (replayedState, error) {
	if err := ctx.Err(); err != nil {
		return replayedState{}, err
	}
	files, err := store.openFiles()
	if err != nil {
		return replayedState{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	defer files.Close()
	unlock, err := lockAnchoredFile(ctx, files.lock, true)
	if err != nil {
		return replayedState{}, err
	}
	defer unlock()
	return store.readUnlocked(files.events)
}

func (store *FileStore) openFiles() (*anchoredFiles, error) {
	return openAnchoredFiles(store.root)
}

func (store *FileStore) readUnlocked(file *os.File) (replayedState, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return replayedState{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	contents, err := io.ReadAll(file)
	if err != nil {
		return replayedState{}, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		return replayedState{}, fmt.Errorf("%w: truncated final event", ErrHistoryCorrupt)
	}
	lines := bytes.Split(contents, []byte{'\n'})
	events := make([]event, 0, len(lines))
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		if len(line) >= maxEventLineBytes {
			return replayedState{}, fmt.Errorf("%w: line %d exceeds 16 MiB", ErrHistoryCorrupt, index+1)
		}
		var entry event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&entry); err != nil {
			return replayedState{}, fmt.Errorf("%w: decode sequence %d: %v", ErrHistoryCorrupt, index+1, err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return replayedState{}, fmt.Errorf("%w: trailing data at sequence %d", ErrHistoryCorrupt, index+1)
		}
		events = append(events, entry)
	}
	return replay(events)
}

func replay(events []event) (replayedState, error) {
	state := replayedState{
		assignments: make(map[string]ExecutionAssignment),
		receipts:    make(map[string]ExecutionReceipt),
		receiptIDs:  make(map[string]string),
		events:      append([]event(nil), events...),
	}
	previousHash := ""
	for index, entry := range events {
		sequence := uint64(index + 1)
		if entry.Sequence != sequence || entry.PreviousHash != previousHash {
			return replayedState{}, fmt.Errorf("%w: invalid chain at sequence %d", ErrHistoryCorrupt, sequence)
		}
		wantHash, err := hashEvent(entry)
		if err != nil || entry.Hash != wantHash {
			return replayedState{}, fmt.Errorf("%w: invalid hash at sequence %d", ErrHistoryCorrupt, sequence)
		}
		assignment, err := canonicalAssignment(entry.Assignment)
		if err != nil || assignment != entry.Assignment {
			return replayedState{}, fmt.Errorf("%w: invalid assignment at sequence %d", ErrHistoryCorrupt, sequence)
		}
		switch entry.Kind {
		case eventAssignmentRegistered:
			if entry.Receipt != nil || assignment.State != AssignmentPrepared || assignment.MatrixDispatchEventID != "" {
				return replayedState{}, fmt.Errorf("%w: invalid registration at sequence %d", ErrHistoryCorrupt, sequence)
			}
			if _, exists := state.assignments[assignment.ID]; exists {
				return replayedState{}, fmt.Errorf("%w: duplicate assignment at sequence %d", ErrHistoryCorrupt, sequence)
			}
		case eventAssignmentDispatched:
			if entry.Receipt != nil || assignment.State != AssignmentDispatched || assignment.MatrixDispatchEventID == "" {
				return replayedState{}, fmt.Errorf("%w: invalid dispatch at sequence %d", ErrHistoryCorrupt, sequence)
			}
			prior, exists := state.assignments[assignment.ID]
			if !exists || prior.State != AssignmentPrepared {
				return replayedState{}, fmt.Errorf("%w: invalid dispatch transition at sequence %d", ErrHistoryCorrupt, sequence)
			}
			expected := prior
			expected.State = AssignmentDispatched
			expected.MatrixDispatchEventID = assignment.MatrixDispatchEventID
			if expected != assignment {
				return replayedState{}, fmt.Errorf("%w: divergent dispatch at sequence %d", ErrHistoryCorrupt, sequence)
			}
		case eventReceiptSubmitted:
			if entry.Receipt == nil || assignment.State != AssignmentReportSubmitted {
				return replayedState{}, fmt.Errorf("%w: invalid receipt at sequence %d", ErrHistoryCorrupt, sequence)
			}
			prior, exists := state.assignments[assignment.ID]
			if !exists || prior.State != AssignmentDispatched {
				return replayedState{}, fmt.Errorf("%w: invalid receipt transition at sequence %d", ErrHistoryCorrupt, sequence)
			}
			expected := prior
			expected.State = AssignmentReportSubmitted
			if expected != assignment {
				return replayedState{}, fmt.Errorf("%w: divergent receipt assignment at sequence %d", ErrHistoryCorrupt, sequence)
			}
			receipt, err := canonicalReceipt(*entry.Receipt)
			if err != nil || !equalReceipts(receipt, *entry.Receipt) || receipt.AssignmentID != assignment.ID {
				return replayedState{}, fmt.Errorf("%w: invalid receipt payload at sequence %d", ErrHistoryCorrupt, sequence)
			}
			if _, exists := state.receipts[assignment.ID]; exists {
				return replayedState{}, fmt.Errorf("%w: duplicate receipt at sequence %d", ErrHistoryCorrupt, sequence)
			}
			if _, exists := state.receiptIDs[receipt.ID]; exists {
				return replayedState{}, fmt.Errorf("%w: duplicate receipt ID at sequence %d", ErrHistoryCorrupt, sequence)
			}
			state.receipts[assignment.ID] = cloneReceipt(receipt)
			state.receiptIDs[receipt.ID] = assignment.ID
		default:
			return replayedState{}, fmt.Errorf("%w: unknown event kind at sequence %d", ErrHistoryCorrupt, sequence)
		}
		state.assignments[assignment.ID] = assignment
		previousHash = entry.Hash
	}
	return state, nil
}

func (store *FileStore) appendUnlocked(file *os.File, existing []event, candidate event) error {
	candidate.Sequence = uint64(len(existing) + 1)
	if len(existing) > 0 {
		candidate.PreviousHash = existing[len(existing)-1].Hash
	}
	hash, err := hashEvent(candidate)
	if err != nil {
		return err
	}
	candidate.Hash = hash
	line, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if len(line) >= maxEventLineBytes {
		return fmt.Errorf("%w: event exceeds 16 MiB", ErrHistoryCorrupt)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return nil
}

func canonicalAssignment(value ExecutionAssignment) (ExecutionAssignment, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.ProjectID = strings.TrimSpace(value.ProjectID)
	value.MissionID = strings.TrimSpace(value.MissionID)
	value.GovernanceTaskID = strings.TrimSpace(value.GovernanceTaskID)
	value.RunID = strings.TrimSpace(value.RunID)
	value.WorkItemID = strings.TrimSpace(value.WorkItemID)
	value.EnvironmentID = strings.TrimSpace(value.EnvironmentID)
	value.AgentTeamsInstanceID = strings.TrimSpace(value.AgentTeamsInstanceID)
	value.LeaderLogicalActorID = strings.TrimSpace(value.LeaderLogicalActorID)
	value.LeaderRuntimePrincipalID = strings.TrimSpace(value.LeaderRuntimePrincipalID)
	value.ContextID = strings.TrimSpace(value.ContextID)
	value.ContextHash = strings.TrimSpace(value.ContextHash)
	value.LeaseID = strings.TrimSpace(value.LeaseID)
	value.MissionArtifactURI = strings.TrimSpace(value.MissionArtifactURI)
	value.MissionArtifactSHA256 = strings.TrimSpace(value.MissionArtifactSHA256)
	value.MatrixRoomID = strings.TrimSpace(value.MatrixRoomID)
	value.MatrixDispatchEventID = strings.TrimSpace(value.MatrixDispatchEventID)
	value.CreatedAt = value.CreatedAt.UTC()
	value.Deadline = value.Deadline.UTC()
	if value.ID == "" || value.ProjectID == "" || value.MissionID == "" || value.GovernanceTaskID == "" || value.RunID == "" || value.WorkItemID == "" || value.EnvironmentID == "" || value.AgentTeamsInstanceID == "" || value.LeaderLogicalActorID == "" || value.LeaderRuntimePrincipalID == "" || value.ContextID == "" || value.ContextHash == "" || value.LeaseID == "" || value.MissionArtifactURI == "" || value.MissionArtifactSHA256 == "" || value.MatrixRoomID == "" {
		return ExecutionAssignment{}, ErrAssignmentConflict
	}
	if value.AssignmentRevision <= 0 || value.RuntimeBindingRevision <= 0 || value.GoalVersion <= 0 || value.CreatedAt.IsZero() || value.Deadline.IsZero() || !value.Deadline.After(value.CreatedAt) {
		return ExecutionAssignment{}, ErrAssignmentConflict
	}
	return value, nil
}

func canonicalReceipt(value ExecutionReceipt) (ExecutionReceipt, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.AssignmentID = strings.TrimSpace(value.AssignmentID)
	value.ReportSHA256 = strings.TrimSpace(value.ReportSHA256)
	value.SubmittedAt = value.SubmittedAt.UTC()
	report, digest, err := canonicalReport(value.Report)
	if err != nil {
		return ExecutionReceipt{}, err
	}
	value.Report = report
	if value.ID == "" || value.AssignmentID == "" || value.ReportSHA256 == "" || value.SubmittedAt.IsZero() {
		return ExecutionReceipt{}, ErrReceiptConflict
	}
	if value.ReportSHA256 != digest {
		return ExecutionReceipt{}, ErrReceiptConflict
	}
	return value, nil
}

func canonicalReport(value ExecutionReport) (ExecutionReport, string, error) {
	value.AssignmentID = strings.TrimSpace(value.AssignmentID)
	value.Status = strings.TrimSpace(value.Status)
	value.SummarySHA256 = strings.TrimSpace(value.SummarySHA256)
	value.Artifacts = append([]Artifact(nil), value.Artifacts...)
	for index := range value.Artifacts {
		value.Artifacts[index].Kind = strings.TrimSpace(value.Artifacts[index].Kind)
		value.Artifacts[index].URI = strings.TrimSpace(value.Artifacts[index].URI)
		value.Artifacts[index].SHA256 = strings.TrimSpace(value.Artifacts[index].SHA256)
		value.Artifacts[index].EnvironmentID = strings.TrimSpace(value.Artifacts[index].EnvironmentID)
	}
	sort.Slice(value.Artifacts, func(left, right int) bool {
		return artifactLess(value.Artifacts[left], value.Artifacts[right])
	})
	value.Checks = append([]Check(nil), value.Checks...)
	for index := range value.Checks {
		value.Checks[index].Name = strings.TrimSpace(value.Checks[index].Name)
		value.Checks[index].Status = strings.TrimSpace(value.Checks[index].Status)
		value.Checks[index].EvidenceSHA256 = strings.TrimSpace(value.Checks[index].EvidenceSHA256)
	}
	sort.Slice(value.Checks, func(left, right int) bool {
		return checkLess(value.Checks[left], value.Checks[right])
	})
	if value.AssignmentID == "" || value.Status == "" || value.SummarySHA256 == "" {
		return ExecutionReport{}, "", ErrReceiptConflict
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ExecutionReport{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return value, hex.EncodeToString(digest[:]), nil
}

func artifactLess(left, right Artifact) bool {
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.URI != right.URI {
		return left.URI < right.URI
	}
	if left.SHA256 != right.SHA256 {
		return left.SHA256 < right.SHA256
	}
	if left.EnvironmentID != right.EnvironmentID {
		return left.EnvironmentID < right.EnvironmentID
	}
	return left.Size < right.Size
}

func checkLess(left, right Check) bool {
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Status != right.Status {
		return left.Status < right.Status
	}
	return left.EvidenceSHA256 < right.EvidenceSHA256
}

func hashEvent(value event) (string, error) {
	value.Hash = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func equalReports(left, right ExecutionReport) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func equalReceipts(left, right ExecutionReceipt) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func cloneReceipt(value ExecutionReceipt) ExecutionReceipt {
	value.Report.Artifacts = append([]Artifact(nil), value.Report.Artifacts...)
	value.Report.Checks = append([]Check(nil), value.Report.Checks...)
	return value
}

func terminalAssignmentState(state AssignmentState) bool {
	switch state {
	case AssignmentVerified, AssignmentRejected, AssignmentExpired, AssignmentDispatchFailed:
		return true
	default:
		return false
	}
}

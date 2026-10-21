package executionreceipt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestStoreRegistersDispatchesAndReloadsAssignment(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	assignment := validAssignment()
	registered, err := store.Register(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if registered.State != AssignmentPrepared {
		t.Fatalf("registered state = %q, want %q", registered.State, AssignmentPrepared)
	}
	dispatched, err := store.MarkDispatched(context.Background(), assignment.ID, "$matrix-dispatch")
	if err != nil {
		t.Fatal(err)
	}
	if dispatched.State != AssignmentDispatched || dispatched.MatrixDispatchEventID != "$matrix-dispatch" {
		t.Fatalf("dispatched assignment = %#v", dispatched)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Assignment(context.Background(), assignment.ID)
	if err != nil || got.State != AssignmentDispatched {
		t.Fatalf("assignment = %#v, err = %v", got, err)
	}
	if _, err := reopened.MarkDispatched(context.Background(), assignment.ID, "$matrix-dispatch"); err != nil {
		t.Fatalf("idempotent MarkDispatched() error = %v", err)
	}
	if _, err := reopened.MarkDispatched(context.Background(), assignment.ID, "$different-event"); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("divergent MarkDispatched() error = %v, want ErrAssignmentConflict", err)
	}
}

func TestStoreRejectsDivergentAssignmentAndCorruptChain(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	assignment := validAssignment()
	stored, err := store.Register(context.Background(), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := store.Register(context.Background(), assignment); err != nil || duplicate != stored {
		t.Fatalf("idempotent Register() = %#v, %v", duplicate, err)
	}
	divergent := assignment
	divergent.ContextHash = strings.Repeat("b", 64)
	if _, err := store.Register(context.Background(), divergent); !errors.Is(err, ErrAssignmentConflict) {
		t.Fatalf("divergent Register() error = %v, want ErrAssignmentConflict", err)
	}

	path := filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(contents, []byte(`"project_id":"PRJ-001"`), []byte(`"project_id":"PRJ-002"`), 1)
	if bytes.Equal(corrupt, contents) {
		t.Fatal("test did not modify persisted assignment")
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assignment(context.Background(), assignment.ID); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("Assignment() after corruption error = %v, want ErrHistoryCorrupt", err)
	}
}

func TestStoreReturnsOriginalReceiptForSameCanonicalReport(t *testing.T) {
	root := t.TempDir()
	store := dispatchedStore(t, root)
	first := validReceipt("ERC-001")
	stored, err := store.Submit(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	retry := validReceipt("ERC-RETRY")
	retry.SubmittedAt = retry.SubmittedAt.Add(time.Hour)
	retry.Report.Artifacts[0], retry.Report.Artifacts[1] = retry.Report.Artifacts[1], retry.Report.Artifacts[0]
	retry.Report.Checks[0], retry.Report.Checks[1] = retry.Report.Checks[1], retry.Report.Checks[0]
	retry.ReportSHA256 = reportDigest(retry.Report)
	duplicate, err := store.Submit(context.Background(), retry)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != stored.ID || duplicate.SubmittedAt != stored.SubmittedAt {
		t.Fatalf("idempotent Submit() = %#v, want original %#v", duplicate, stored)
	}
	assignment, err := store.Assignment(context.Background(), first.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != AssignmentReportSubmitted {
		t.Fatalf("assignment state = %q, want %q", assignment.State, AssignmentReportSubmitted)
	}
}

func TestStoreCanonicalizesNULTupleCollisionsByField(t *testing.T) {
	store := dispatchedStore(t, t.TempDir())
	first := validReceipt("ERC-NUL-FIRST")
	first.Report.Artifacts = []Artifact{
		{Kind: "a", URI: "b\x00c", SHA256: "d", EnvironmentID: "e", Size: 1},
		{Kind: "a\x00b", URI: "c", SHA256: "d", EnvironmentID: "e", Size: 1},
	}
	first.Report.Checks = []Check{
		{Name: "a", Status: "b\x00c", EvidenceSHA256: "d"},
		{Name: "a\x00b", Status: "c", EvidenceSHA256: "d"},
	}
	firstCanonical, firstDigest, err := canonicalReport(first.Report)
	if err != nil {
		t.Fatal(err)
	}
	first.ReportSHA256 = firstDigest
	stored, err := store.Submit(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}

	retry := validReceipt("ERC-NUL-RETRY")
	retry.Report = first.Report
	retry.Report.Artifacts[0], retry.Report.Artifacts[1] = retry.Report.Artifacts[1], retry.Report.Artifacts[0]
	retry.Report.Checks[0], retry.Report.Checks[1] = retry.Report.Checks[1], retry.Report.Checks[0]
	retryCanonical, retryDigest, err := canonicalReport(retry.Report)
	if err != nil {
		t.Fatal(err)
	}
	retry.ReportSHA256 = retryDigest
	duplicate, err := store.Submit(context.Background(), retry)
	if err != nil {
		t.Fatalf("Submit() for reordered NUL tuples error = %v", err)
	}
	if duplicate.ID != stored.ID {
		t.Fatalf("duplicate receipt ID = %q, want original %q", duplicate.ID, stored.ID)
	}
	if !reflect.DeepEqual(firstCanonical, retryCanonical) || firstDigest != retryDigest {
		t.Fatalf("canonical reports differ:\nfirst=%#v %s\nretry=%#v %s", firstCanonical, firstDigest, retryCanonical, retryDigest)
	}
}

func TestStoreRejectsDifferentReportForSameAssignment(t *testing.T) {
	store := dispatchedStore(t, t.TempDir())
	first := validReceipt("ERC-001")
	if _, err := store.Submit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	changed := validReceipt("ERC-002")
	changed.Report.Status = "failed"
	changed.ReportSHA256 = reportDigest(changed.Report)
	if _, err := store.Submit(context.Background(), changed); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("divergent Submit() error = %v, want ErrReceiptConflict", err)
	}
}

func TestStoreRejectsExpiredOrTerminalAssignment(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	expired := validAssignment()
	expired.ID = "ASN-EXPIRED"
	expired.Deadline = time.Now().UTC().Add(-time.Minute)
	if _, err := store.Register(context.Background(), expired); !errors.Is(err, ErrAssignmentExpired) {
		t.Fatalf("expired Register() error = %v, want ErrAssignmentExpired", err)
	}
	terminal := validAssignment()
	terminal.ID = "ASN-TERMINAL"
	terminal.State = AssignmentVerified
	if _, err := store.Register(context.Background(), terminal); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal Register() error = %v, want ErrInvalidTransition", err)
	}
	prepared := validAssignment()
	prepared.ID = "ASN-PREPARED"
	if _, err := store.Register(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	receipt := validReceipt("ERC-PREPARED")
	receipt.AssignmentID = prepared.ID
	receipt.Report.AssignmentID = prepared.ID
	receipt.ReportSHA256 = reportDigest(receipt.Report)
	if _, err := store.Submit(context.Background(), receipt); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Submit() before dispatch error = %v, want ErrInvalidTransition", err)
	}
}

func TestStoreReadersObserveAnotherProcessAppend(t *testing.T) {
	root := t.TempDir()
	reader := dispatchedStore(t, root)
	if _, err := reader.Receipt(context.Background(), validAssignment().ID); !errors.Is(err, ErrReceiptNotFound) {
		t.Fatalf("initial Receipt() error = %v, want ErrReceiptNotFound", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestStoreProcessHelper$")
	command.Env = append(os.Environ(), "HAOWORK_RECEIPT_HELPER=1", "HAOWORK_RECEIPT_ROOT="+root)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Wait(context.Background(), validAssignment().ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output.Bytes())
	}
	if got.ID != "ERC-PROCESS" {
		t.Fatalf("receipt ID = %q, want ERC-PROCESS", got.ID)
	}
}

func TestStoreWaitReloadsUntilAnotherWriterSubmits(t *testing.T) {
	root := t.TempDir()
	waiter := dispatchedStore(t, root)
	writer, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, submitErr := writer.Submit(context.Background(), validReceipt("ERC-WAIT"))
		written <- submitErr
	}()
	receipt, err := waiter.Wait(context.Background(), validAssignment().ID, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID != "ERC-WAIT" {
		t.Fatalf("receipt ID = %q, want ERC-WAIT", receipt.ID)
	}
	if err := <-written; err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
}

func TestStoreProcessHelper(t *testing.T) {
	if os.Getenv("HAOWORK_RECEIPT_HELPER") != "1" {
		t.Skip("helper process only")
	}
	store, err := Open(os.Getenv("HAOWORK_RECEIPT_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Submit(context.Background(), validReceipt("ERC-PROCESS")); err != nil {
		t.Fatal(err)
	}
}

func TestStoreTightensStoragePermissions(t *testing.T) {
	root := t.TempDir()
	trustRoot := filepath.Join(root, ".haowork")
	directory := filepath.Join(trustRoot, "execution-receipts")
	if err := os.MkdirAll(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(directory, "events.jsonl")
	lockPath := filepath.Join(directory, "events.lock")
	for _, path := range []string{eventsPath, lockPath} {
		if err := os.WriteFile(path, nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(context.Background(), validAssignment()); err != nil {
		t.Fatal(err)
	}
	assertTrustRootPermissions(t, trustRoot)
	assertRestrictedPermissions(t, directory, true)
	assertRestrictedPermissions(t, eventsPath, false)
	assertRestrictedPermissions(t, lockPath, false)
}

func TestStoreDoesNotChangeProjectRootPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	before := projectRootPermissionSnapshot(t, root)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(context.Background(), validAssignment()); err != nil {
		t.Fatal(err)
	}
	after := projectRootPermissionSnapshot(t, root)
	if after != before {
		t.Fatalf("project root permissions changed from %q to %q", before, after)
	}
}

func TestStoreRejectsNonRegularStoragePaths(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "receipt directory is a regular file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, ".haowork"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".haowork", "execution-receipts"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "events path is a directory",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "lock path is a directory",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, ".haowork", "execution-receipts", "events.lock"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Register(context.Background(), validAssignment())
			if !errors.Is(err, ErrUnsafeStorePath) {
				t.Fatalf("Register() error = %v, want explicit unsafe-path rejection", err)
			}
		})
	}
}

func TestStoreRejectsSymlinkedStoragePaths(t *testing.T) {
	tests := []struct {
		name       string
		linkName   string
		directory  bool
		assertSafe func(*testing.T, string)
	}{
		{
			name:      "receipt directory",
			linkName:  "execution-receipts",
			directory: true,
			assertSafe: func(t *testing.T, target string) {
				t.Helper()
				if _, err := os.Stat(filepath.Join(target, "events.jsonl")); !os.IsNotExist(err) {
					t.Fatalf("directory symlink target was opened or created: %v", err)
				}
			},
		},
		{
			name:     "events file",
			linkName: filepath.Join("execution-receipts", "events.jsonl"),
		},
		{
			name:     "lock file",
			linkName: filepath.Join("execution-receipts", "events.lock"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, ".haowork")
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "target")
			if test.directory {
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Join(base, "execution-receipts"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(base, test.linkName)
			if err := os.Symlink(outside, link); err != nil {
				if symlinkPrivilegeUnavailable(err) {
					t.Skipf("symbolic-link privilege unavailable: %v", err)
				}
				t.Fatal(err)
			}
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Register(context.Background(), validAssignment())
			if !errors.Is(err, ErrUnsafeStorePath) {
				t.Fatalf("Register() error = %v, want symlink rejection", err)
			}
			if test.assertSafe != nil {
				test.assertSafe(t, outside)
			} else if contents, err := os.ReadFile(outside); err != nil || string(contents) != "unchanged" {
				t.Fatalf("file symlink target changed: contents=%q err=%v", contents, err)
			}
		})
	}
}

func TestStoreRejectsSymlinkedTrustRoot(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	trustRoot := filepath.Join(root, ".haowork")
	if err := os.Symlink(target, trustRoot); err != nil {
		if symlinkPrivilegeUnavailable(err) {
			t.Skipf("symbolic-link privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(context.Background(), validAssignment()); !errors.Is(err, ErrUnsafeStorePath) {
		t.Fatalf("Register() error = %v, want trust-root symlink rejection", err)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("trust-root symlink target was touched: entries=%v err=%v", entries, err)
	}
}

func TestStoreRejectsTruncatedTail(t *testing.T) {
	root := t.TempDir()
	store := dispatchedStore(t, root)
	path := filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"sequence":99`); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assignment(context.Background(), validAssignment().ID); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("Assignment() error = %v, want ErrHistoryCorrupt", err)
	}
}

func TestStoreRejectsDamagedMiddleChainField(t *testing.T) {
	root := t.TempDir()
	store := dispatchedStore(t, root)
	if _, err := store.Submit(context.Background(), validReceipt("ERC-MIDDLE")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".haowork", "execution-receipts", "events.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(contents, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 3 {
		t.Fatalf("event lines = %d, want 3", len(lines))
	}
	var middle event
	if err := json.Unmarshal(lines[1], &middle); err != nil {
		t.Fatal(err)
	}
	middle.PreviousHash = strings.Repeat("0", 64)
	lines[1], err = json.Marshal(middle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte{'\n'}), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Receipt(context.Background(), validAssignment().ID); !errors.Is(err, ErrHistoryCorrupt) {
		t.Fatalf("Receipt() error = %v, want ErrHistoryCorrupt", err)
	}
}

func TestStoreLockContentionHonorsContextAndRecovers(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(context.Background(), validAssignment()); err != nil {
		t.Fatal(err)
	}
	blocker := flock.New(store.lockPath)
	if err := blocker.Lock(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := store.MarkDispatched(ctx, validAssignment().ID, "$after-contention"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("MarkDispatched() error = %v, want context deadline", err)
	}
	if err := blocker.Unlock(); err != nil {
		t.Fatal(err)
	}
	got, err := store.MarkDispatched(context.Background(), validAssignment().ID, "$after-contention")
	if err != nil {
		t.Fatalf("MarkDispatched() after unlock error = %v", err)
	}
	if got.State != AssignmentDispatched {
		t.Fatalf("state after unlock = %q, want %q", got.State, AssignmentDispatched)
	}
}

func validAssignment() ExecutionAssignment {
	return ExecutionAssignment{
		ID:                       "ASN-001",
		ProjectID:                "PRJ-001",
		MissionID:                "MSN-001",
		GovernanceTaskID:         "TSK-001",
		RunID:                    "RUN-001",
		WorkItemID:               "WKI-001",
		EnvironmentID:            "ENV-001",
		AgentTeamsInstanceID:     "ATI-001",
		LeaderLogicalActorID:     "AGT-LEADER",
		LeaderRuntimePrincipalID: "RTP-LEADER",
		AssignmentRevision:       1,
		RuntimeBindingRevision:   1,
		GoalVersion:              1,
		ContextID:                "CTX-001",
		ContextHash:              strings.Repeat("c", 64),
		LeaseID:                  "LSE-001",
		MissionArtifactURI:       "s3://haowork/environments/ENV-001/missions/MSN-001/artifacts/" + strings.Repeat("d", 64),
		MissionArtifactSHA256:    strings.Repeat("d", 64),
		MatrixRoomID:             "!leader:example.invalid",
		State:                    AssignmentPrepared,
		CreatedAt:                time.Date(2026, 9, 5, 1, 2, 3, 0, time.FixedZone("test", 8*60*60)),
		Deadline:                 time.Now().UTC().Add(time.Hour),
	}
}

func validReceipt(id string) ExecutionReceipt {
	report := ExecutionReport{
		AssignmentID:  "ASN-001",
		Status:        "succeeded",
		SummarySHA256: strings.Repeat("e", 64),
		Artifacts: []Artifact{
			{Kind: "report", URI: "s3://haowork/ENV-001/MSN-001/z.json", SHA256: strings.Repeat("f", 64), EnvironmentID: "ENV-001", Size: 22},
			{Kind: "log", URI: "s3://haowork/ENV-001/MSN-001/a.txt", SHA256: strings.Repeat("a", 64), EnvironmentID: "ENV-001", Size: 11},
		},
		Checks: []Check{
			{Name: "unit-tests", Status: "passed", EvidenceSHA256: strings.Repeat("1", 64)},
			{Name: "lint", Status: "passed", EvidenceSHA256: strings.Repeat("2", 64)},
		},
	}
	return ExecutionReceipt{
		ID:           id,
		AssignmentID: report.AssignmentID,
		ReportSHA256: reportDigest(report),
		Report:       report,
		SubmittedAt:  time.Date(2026, 9, 5, 2, 3, 4, 0, time.FixedZone("test", 8*60*60)),
	}
}

func dispatchedStore(t *testing.T, root string) *FileStore {
	t.Helper()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	assignment := validAssignment()
	if _, err := store.Register(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDispatched(context.Background(), assignment.ID, "$matrix-dispatch"); err != nil {
		t.Fatal(err)
	}
	return store
}

func reportDigest(report ExecutionReport) string {
	report.AssignmentID = strings.TrimSpace(report.AssignmentID)
	report.Status = strings.TrimSpace(report.Status)
	report.SummarySHA256 = strings.TrimSpace(report.SummarySHA256)
	report.Artifacts = append([]Artifact(nil), report.Artifacts...)
	for index := range report.Artifacts {
		report.Artifacts[index].Kind = strings.TrimSpace(report.Artifacts[index].Kind)
		report.Artifacts[index].URI = strings.TrimSpace(report.Artifacts[index].URI)
		report.Artifacts[index].SHA256 = strings.TrimSpace(report.Artifacts[index].SHA256)
		report.Artifacts[index].EnvironmentID = strings.TrimSpace(report.Artifacts[index].EnvironmentID)
	}
	sort.Slice(report.Artifacts, func(left, right int) bool {
		leftArtifact, rightArtifact := report.Artifacts[left], report.Artifacts[right]
		if leftArtifact.Kind != rightArtifact.Kind {
			return leftArtifact.Kind < rightArtifact.Kind
		}
		if leftArtifact.URI != rightArtifact.URI {
			return leftArtifact.URI < rightArtifact.URI
		}
		if leftArtifact.SHA256 != rightArtifact.SHA256 {
			return leftArtifact.SHA256 < rightArtifact.SHA256
		}
		if leftArtifact.EnvironmentID != rightArtifact.EnvironmentID {
			return leftArtifact.EnvironmentID < rightArtifact.EnvironmentID
		}
		return leftArtifact.Size < rightArtifact.Size
	})
	report.Checks = append([]Check(nil), report.Checks...)
	for index := range report.Checks {
		report.Checks[index].Name = strings.TrimSpace(report.Checks[index].Name)
		report.Checks[index].Status = strings.TrimSpace(report.Checks[index].Status)
		report.Checks[index].EvidenceSHA256 = strings.TrimSpace(report.Checks[index].EvidenceSHA256)
	}
	sort.Slice(report.Checks, func(left, right int) bool {
		leftCheck, rightCheck := report.Checks[left], report.Checks[right]
		if leftCheck.Name != rightCheck.Name {
			return leftCheck.Name < rightCheck.Name
		}
		if leftCheck.Status != rightCheck.Status {
			return leftCheck.Status < rightCheck.Status
		}
		return leftCheck.EvidenceSHA256 < rightCheck.EvidenceSHA256
	})
	encoded, err := json.Marshal(report)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

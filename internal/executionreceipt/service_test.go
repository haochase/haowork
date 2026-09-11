package executionreceipt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/trace"
)

func TestServiceAcceptsOnlyBoundDeliveryLeader(t *testing.T) {
	service, store, traces, now := serviceFixture(t)

	receipt, err := service.Submit(context.Background(), validPrincipal(), validServiceReport())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID == "" || receipt.AssignmentID != "ASN-001" || receipt.Report.Status != "succeeded" {
		t.Fatalf("Submit() = %#v", receipt)
	}
	assignment, err := store.Assignment(context.Background(), receipt.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != AssignmentReportSubmitted {
		t.Fatalf("assignment state = %q, want %q", assignment.State, AssignmentReportSubmitted)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("trace count = %d, want 1", len(records))
	}
	record := records[0]
	if record.SourceEventType != "execution.report.submitted" || record.Status != "candidate" || record.ReportStatus != "succeeded" {
		t.Fatalf("trace status = %#v", record)
	}
	if record.AssignmentID != assignment.ID || record.AssignmentRevision != assignment.AssignmentRevision || record.GoalVersion != assignment.GoalVersion || record.ContextID != assignment.ContextID || record.ContextHash != assignment.ContextHash || record.LeaseID != assignment.LeaseID {
		t.Fatalf("trace assignment binding = %#v", record)
	}
	if record.StartedAt != now || record.FinishedAt != now {
		t.Fatalf("trace time = %s/%s, want %s", record.StartedAt, record.FinishedAt, now)
	}
}

func TestServiceRejectsManagerOrReplacementLeader(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Principal)
		wantError error
	}{
		{name: "manager function", mutate: func(value *Principal) { value.Function = model.FunctionManager }, wantError: ErrPrincipalMismatch},
		{name: "replacement logical actor", mutate: func(value *Principal) { value.LogicalActorID = "AGT-REPLACEMENT" }, wantError: ErrPrincipalMismatch},
		{name: "replacement runtime principal", mutate: func(value *Principal) { value.RuntimePrincipalID = "RTP-REPLACEMENT" }, wantError: ErrPrincipalMismatch},
		{name: "wrong environment", mutate: func(value *Principal) { value.EnvironmentID = "ENV-OTHER" }, wantError: ErrPrincipalMismatch},
		{name: "wrong AgentTeams instance", mutate: func(value *Principal) { value.AgentTeamsInstanceID = "ATI-OTHER" }, wantError: ErrPrincipalMismatch},
		{name: "replacement binding revision", mutate: func(value *Principal) { value.BindingRevision++ }, wantError: ErrBindingRevisionMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, store, traces, _ := serviceFixture(t)
			principal := validPrincipal()
			test.mutate(&principal)
			if _, err := service.Submit(context.Background(), principal, validServiceReport()); !errors.Is(err, test.wantError) {
				t.Fatalf("Submit() error = %v, want %v", err, test.wantError)
			}
			if _, err := store.Receipt(context.Background(), "ASN-001"); !errors.Is(err, ErrReceiptNotFound) {
				t.Fatalf("Receipt() error = %v, want %v", err, ErrReceiptNotFound)
			}
			records, err := traces.ReadAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 0 {
				t.Fatalf("trace count = %d, want 0", len(records))
			}
		})
	}
}

func TestServiceRejectsExpiredLeaseAndAssignment(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name      string
		mutate    func(*ExecutionAssignment)
		wantError error
	}{
		{name: "missing lease", mutate: func(value *ExecutionAssignment) { value.LeaseID = "" }, wantError: ErrLeaseInvalid},
		{name: "expired effective lease deadline", mutate: func(value *ExecutionAssignment) { value.Deadline = now }, wantError: ErrAssignmentInactive},
		{name: "prepared assignment", mutate: func(value *ExecutionAssignment) { value.State = AssignmentPrepared }, wantError: ErrAssignmentInactive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assignment := validAssignment()
			assignment.State = AssignmentDispatched
			assignment.Deadline = now.Add(time.Hour)
			test.mutate(&assignment)
			store := &staticServiceStore{assignment: assignment}
			traces := &memoryTraceStore{}
			service := Service{Store: store, Trace: traces, Clock: func() time.Time { return now }}
			if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); !errors.Is(err, test.wantError) {
				t.Fatalf("Submit() error = %v, want %v", err, test.wantError)
			}
			if store.submitCalls != 0 || len(traces.records) != 0 {
				t.Fatalf("side effects = store %d trace %d, want none", store.submitCalls, len(traces.records))
			}
		})
	}
}

func TestServiceValidatesArtifactMetadataWithoutTrustingCompletion(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExecutionReport)
	}{
		{name: "unknown report status", mutate: func(value *ExecutionReport) { value.Status = "completed" }},
		{name: "invalid summary digest", mutate: func(value *ExecutionReport) { value.SummarySHA256 = strings.Repeat("A", 64) }},
		{name: "foreign bucket", mutate: func(value *ExecutionReport) {
			value.Artifacts[0].URI = "s3://other/environments/ENV-001/missions/MSN-001/artifacts/" + strings.Repeat("f", 64)
		}},
		{name: "foreign mission key", mutate: func(value *ExecutionReport) {
			value.Artifacts[0].URI = "s3://haowork/environments/ENV-001/missions/MSN-OTHER/artifacts/" + strings.Repeat("f", 64)
		}},
		{name: "path traversal", mutate: func(value *ExecutionReport) {
			value.Artifacts[0].URI = "s3://haowork/environments/ENV-001/missions/MSN-001/artifacts/../private"
		}},
		{name: "encoded traversal", mutate: func(value *ExecutionReport) {
			value.Artifacts[0].URI = "s3://haowork/environments/ENV-001/missions/MSN-001/artifacts/%2e%2e%2fprivate"
		}},
		{name: "encoded digest", mutate: func(value *ExecutionReport) {
			value.Artifacts[0].URI = "s3://haowork/environments/ENV-001/missions/MSN-001/artifacts/%66" + strings.Repeat("f", 63)
		}},
		{name: "foreign environment metadata", mutate: func(value *ExecutionReport) { value.Artifacts[0].EnvironmentID = "ENV-OTHER" }},
		{name: "invalid artifact digest", mutate: func(value *ExecutionReport) { value.Artifacts[0].SHA256 = strings.Repeat("F", 64) }},
		{name: "negative artifact size", mutate: func(value *ExecutionReport) { value.Artifacts[0].Size = -1 }},
		{name: "oversized artifact", mutate: func(value *ExecutionReport) { value.Artifacts[0].Size = 16*1024*1024 + 1 }},
		{name: "invalid check status", mutate: func(value *ExecutionReport) { value.Checks[0].Status = "succeeded" }},
		{name: "invalid check digest", mutate: func(value *ExecutionReport) { value.Checks[0].EvidenceSHA256 = "short" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, store, traces, _ := serviceFixture(t)
			report := validServiceReport()
			test.mutate(&report)
			if _, err := service.Submit(context.Background(), validPrincipal(), report); !errors.Is(err, ErrReceiptSchemaInvalid) {
				t.Fatalf("Submit() error = %v, want %v", err, ErrReceiptSchemaInvalid)
			}
			if _, err := store.Receipt(context.Background(), "ASN-001"); !errors.Is(err, ErrReceiptNotFound) {
				t.Fatalf("Receipt() error = %v, want %v", err, ErrReceiptNotFound)
			}
			records, err := traces.ReadAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 0 {
				t.Fatalf("trace count = %d, want 0", len(records))
			}
		})
	}
}

func TestServiceRejectsUnsafeAssignmentArtifactScope(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	assignment := validAssignment()
	assignment.State = AssignmentDispatched
	assignment.Deadline = now.Add(time.Hour)
	assignment.MissionID = "MSN-001/../MSN-OTHER"
	assignment.MissionArtifactURI = "s3://haowork/environments/ENV-001/missions/MSN-001/../MSN-OTHER/artifacts/" + assignment.MissionArtifactSHA256
	store := &staticServiceStore{assignment: assignment}
	traces := &memoryTraceStore{}
	service := Service{Store: store, Trace: traces, Clock: func() time.Time { return now }}
	report := validServiceReport()
	report.Artifacts[0].URI = "s3://haowork/environments/ENV-001/missions/MSN-001/../MSN-OTHER/artifacts/" + report.Artifacts[0].SHA256

	if _, err := service.Submit(context.Background(), validPrincipal(), report); !errors.Is(err, ErrReceiptSchemaInvalid) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrReceiptSchemaInvalid)
	}
	if store.submitCalls != 0 || len(traces.records) != 0 {
		t.Fatalf("unsafe assignment side effects = store %d trace %d", store.submitCalls, len(traces.records))
	}
}

func TestServiceReturnsSameReceiptForIdempotentRetry(t *testing.T) {
	service, _, traces, _ := serviceFixture(t)
	first, err := service.Submit(context.Background(), validPrincipal(), validServiceReport())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Submit(context.Background(), validPrincipal(), validServiceReport())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.ReportSHA256 != second.ReportSHA256 || !first.SubmittedAt.Equal(second.SubmittedAt) {
		t.Fatalf("retry receipt differs: first=%#v second=%#v", first, second)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("trace count = %d, want 1", len(records))
	}
}

func TestServiceRejectsDivergentRetry(t *testing.T) {
	service, _, traces, _ := serviceFixture(t)
	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); err != nil {
		t.Fatal(err)
	}
	divergent := validServiceReport()
	divergent.SummarySHA256 = strings.Repeat("9", 64)
	if _, err := service.Submit(context.Background(), validPrincipal(), divergent); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrReceiptConflict)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("trace count = %d, want 1", len(records))
	}
}

func TestSubmittedReceiptDoesNotMarkWorkItemComplete(t *testing.T) {
	service, _, traces, _ := serviceFixture(t)
	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); err != nil {
		t.Fatal(err)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projection, err := trace.Replay(records)
	if err != nil {
		t.Fatal(err)
	}
	current, exists := projection.Current(records[0].ID)
	if !exists {
		t.Fatal("candidate trace missing from projection")
	}
	if current.Status == string(model.StatusCompleted) || current.SourceEventType == "task.completed" {
		t.Fatalf("receipt trace claims completion: %#v", current)
	}
	if !current.Candidate || current.Status != "candidate" || current.ReportStatus != "succeeded" {
		t.Fatalf("receipt trace is not explicitly candidate-only: %#v", current)
	}
}

func TestServiceFailsClosedWhenTraceAppendFails(t *testing.T) {
	service, store, traces, _ := serviceFixture(t)
	service.Trace = rejectingTraceStore{err: errors.New("trace unavailable")}

	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); !errors.Is(err, ErrReceiptStoreFailed) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrReceiptStoreFailed)
	}
	persisted, err := store.Receipt(context.Background(), "ASN-001")
	if err != nil {
		t.Fatalf("persisted candidate receipt unavailable for recovery: %v", err)
	}
	assignment, err := store.Assignment(context.Background(), "ASN-001")
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != AssignmentReportSubmitted {
		t.Fatalf("assignment state = %q, want recoverable candidate state", assignment.State)
	}

	service.Trace = traces
	recovered, err := service.Submit(context.Background(), validPrincipal(), validServiceReport())
	if err != nil {
		t.Fatalf("idempotent retry did not repair trace: %v", err)
	}
	if recovered.ID != persisted.ID || !recovered.SubmittedAt.Equal(persisted.SubmittedAt) {
		t.Fatalf("recovered receipt = %#v, want persisted %#v", recovered, persisted)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].SourceEventID != persisted.ID {
		t.Fatalf("recovered traces = %#v", records)
	}
}

func TestServiceRepairsTraceAfterCandidateDeadline(t *testing.T) {
	service, store, traces, now := serviceFixture(t)
	current := now
	service.Clock = func() time.Time { return current }
	service.Trace = rejectingTraceStore{err: errors.New("trace unavailable")}

	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); !errors.Is(err, ErrReceiptStoreFailed) {
		t.Fatalf("first Submit() error = %v, want %v", err, ErrReceiptStoreFailed)
	}
	persisted, err := store.Receipt(context.Background(), "ASN-001")
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := store.Assignment(context.Background(), "ASN-001")
	if err != nil {
		t.Fatal(err)
	}
	current = assignment.Deadline.Add(time.Second)
	service.Trace = traces

	recovered, err := service.Submit(context.Background(), validPrincipal(), validServiceReport())
	if err != nil {
		t.Fatalf("retry after deadline error = %v", err)
	}
	if recovered.ID != persisted.ID || !recovered.SubmittedAt.Equal(persisted.SubmittedAt) {
		t.Fatalf("recovered receipt = %#v, want persisted %#v", recovered, persisted)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "TRC-"+strings.TrimPrefix(persisted.ID, "ERC-") || records[0].SourceEventID != persisted.ID {
		t.Fatalf("recovered traces = %#v", records)
	}
}

func TestServiceRejectsExpiredDispatchedFirstSubmission(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	assignment := validAssignment()
	assignment.State = AssignmentDispatched
	assignment.Deadline = now
	store := &staticServiceStore{assignment: assignment}
	traces := &memoryTraceStore{}
	service := Service{Store: store, Trace: traces, Clock: func() time.Time { return now }}

	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); !errors.Is(err, ErrAssignmentInactive) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrAssignmentInactive)
	}
	if store.submitCalls != 0 || len(traces.records) != 0 {
		t.Fatalf("side effects = store %d trace %d, want none", store.submitCalls, len(traces.records))
	}
}

func TestServiceRejectsDivergentRetryAfterCandidateDeadline(t *testing.T) {
	service, store, traces, now := serviceFixture(t)
	current := now
	service.Clock = func() time.Time { return current }
	if _, err := service.Submit(context.Background(), validPrincipal(), validServiceReport()); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.Assignment(context.Background(), "ASN-001")
	if err != nil {
		t.Fatal(err)
	}
	current = assignment.Deadline.Add(time.Second)
	divergent := validServiceReport()
	divergent.SummarySHA256 = strings.Repeat("9", 64)

	if _, err := service.Submit(context.Background(), validPrincipal(), divergent); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("Submit() error = %v, want %v", err, ErrReceiptConflict)
	}
	records, err := traces.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("trace count = %d, want 1", len(records))
	}
}

func serviceFixture(t *testing.T) (Service, *FileStore, trace.Store, time.Time) {
	t.Helper()
	root := t.TempDir()
	store := dispatchedStore(t, root)
	traces := trace.NewAt(root+"/trace.jsonl", root+"/trace.lock")
	now := time.Now().UTC().Truncate(time.Second)
	return Service{Store: store, Trace: traces, Clock: func() time.Time { return now }}, store, traces, now
}

func validPrincipal() Principal {
	return Principal{
		LogicalActorID:       "AGT-LEADER",
		RuntimePrincipalID:   "RTP-LEADER",
		EnvironmentID:        "ENV-001",
		AgentTeamsInstanceID: "ATI-001",
		BindingRevision:      1,
		Function:             model.FunctionDeliveryLeader,
	}
}

func validServiceReport() ExecutionReport {
	return ExecutionReport{
		AssignmentID:  "ASN-001",
		Status:        "succeeded",
		SummarySHA256: strings.Repeat("e", 64),
		Artifacts: []Artifact{{
			Kind:          "report",
			URI:           "s3://haowork/environments/ENV-001/missions/MSN-001/artifacts/" + strings.Repeat("f", 64),
			SHA256:        strings.Repeat("f", 64),
			EnvironmentID: "ENV-001",
			Size:          1024,
		}},
		Checks: []Check{{Name: "unit-tests", Status: "passed", EvidenceSHA256: strings.Repeat("1", 64)}},
	}
}

type staticServiceStore struct {
	assignment  ExecutionAssignment
	submitCalls int
}

func (store *staticServiceStore) Register(context.Context, ExecutionAssignment) (ExecutionAssignment, error) {
	return ExecutionAssignment{}, errors.New("unexpected Register")
}

func (store *staticServiceStore) MarkDispatched(context.Context, string, string) (ExecutionAssignment, error) {
	return ExecutionAssignment{}, errors.New("unexpected MarkDispatched")
}

func (store *staticServiceStore) Submit(_ context.Context, receipt ExecutionReceipt) (ExecutionReceipt, error) {
	store.submitCalls++
	return receipt, nil
}

func (store *staticServiceStore) Assignment(context.Context, string) (ExecutionAssignment, error) {
	return store.assignment, nil
}

func (store *staticServiceStore) Receipt(context.Context, string) (ExecutionReceipt, error) {
	return ExecutionReceipt{}, ErrReceiptNotFound
}

func (store *staticServiceStore) Wait(context.Context, string, time.Duration) (ExecutionReceipt, error) {
	return ExecutionReceipt{}, errors.New("unexpected Wait")
}

type memoryTraceStore struct {
	records []trace.Envelope
}

func (store *memoryTraceStore) AppendIdempotent(_ context.Context, candidate trace.Envelope) (trace.Envelope, error) {
	store.records = append(store.records, candidate)
	return candidate, nil
}

func (store *memoryTraceStore) ReadAll(context.Context) ([]trace.Envelope, error) {
	return append([]trace.Envelope(nil), store.records...), nil
}

func (store *memoryTraceStore) Since(context.Context, uint64) ([]trace.Envelope, error) {
	return nil, nil
}

type rejectingTraceStore struct {
	err error
}

func (store rejectingTraceStore) AppendIdempotent(context.Context, trace.Envelope) (trace.Envelope, error) {
	return trace.Envelope{}, store.err
}

func (rejectingTraceStore) ReadAll(context.Context) ([]trace.Envelope, error) { return nil, nil }

func (rejectingTraceStore) Since(context.Context, uint64) ([]trace.Envelope, error) { return nil, nil }

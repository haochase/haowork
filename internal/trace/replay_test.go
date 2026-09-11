package trace

import (
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestReplayQueriesCurrentParentChildrenAndRedactionWithoutMutatingHistory(t *testing.T) {
	parent := traceFixture()
	parent.ID, parent.ParentTraceID, parent.Sequence = "TRC-ROOT", "", 1
	parent.ArtifactRefs = nil
	child := traceFixture()
	child.ID, child.SourceEventID, child.ParentTraceID, child.Sequence = "TRC-CHILD", "SRC-CHILD", parent.ID, 2
	child.ArtifactRefs = append(child.ArtifactRefs, artifactFixture())
	projection, err := Replay([]Envelope{parent, child})
	if err != nil {
		t.Fatal(err)
	}
	current, exists := projection.Current(parent.ID)
	if !exists || current.ID != child.ID {
		t.Fatalf("Current() = %#v / %t, want child", current, exists)
	}
	foundParent, exists := projection.Parent(child.ID)
	if !exists || foundParent.ID != parent.ID {
		t.Fatalf("Parent() = %#v / %t, want parent", foundParent, exists)
	}
	children := projection.Children(parent.ID)
	if len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("Children() = %#v, want child", children)
	}
	redacted := Redact(child)
	if redacted.ArtifactRefs[0].URI != "redacted" || child.ArtifactRefs[0].URI == "redacted" {
		t.Fatalf("redaction mutated history or retained URI: redacted=%#v original=%#v", redacted, child)
	}
	child.SenderID = "runtime-secret"
	child.Artifacts = []ArtifactObservation{{Kind: "report", URI: "private/report", SHA256: "hash", EnvironmentID: "private", Size: 7}}
	redacted = Redact(child)
	if redacted.SenderID != "redacted" || redacted.Artifacts[0].URI != "redacted" || child.SenderID != "runtime-secret" {
		t.Fatalf("redacted sender/artifacts = %#v", redacted)
	}
}

func TestReplayRejectsSelfAndCyclicParentChains(t *testing.T) {
	self := traceFixture()
	self.ParentTraceID = self.ID
	if _, err := Replay([]Envelope{self}); err == nil {
		t.Fatal("Replay accepted a self parent")
	}
	left := traceFixture()
	left.ID, left.ParentTraceID = "TRC-LEFT", "TRC-RIGHT"
	right := traceFixture()
	right.ID, right.SourceEventID, right.ParentTraceID = "TRC-RIGHT", "SRC-RIGHT", "TRC-LEFT"
	if _, err := Replay([]Envelope{left, right}); err == nil {
		t.Fatal("Replay accepted a cyclic parent chain")
	}
}

func TestReplayPreservesCandidateReceiptSemantics(t *testing.T) {
	record := traceFixture()
	record.AssignmentID = "ASN-001"
	record.AssignmentRevision = 2
	record.GoalVersion = 3
	record.ContextID = "CTX-001"
	record.ContextHash = "context-hash"
	record.LeaseID = "LSE-001"
	record.ReportSHA256 = "report-hash"
	record.ReportStatus = "succeeded"
	record.Candidate = true
	record.Status = "candidate"
	record.SourceEventType = "execution.report.submitted"

	projection, err := Replay([]Envelope{record})
	if err != nil {
		t.Fatal(err)
	}
	got, exists := projection.Current(record.ID)
	if !exists || got.AssignmentID != record.AssignmentID || got.AssignmentRevision != 2 || got.GoalVersion != 3 || got.ContextID != record.ContextID || got.ContextHash != record.ContextHash || got.LeaseID != record.LeaseID || got.ReportSHA256 != record.ReportSHA256 || got.ReportStatus != "succeeded" || !got.Candidate || got.Status != "candidate" {
		t.Fatalf("candidate receipt projection = %#v", got)
	}
}

func artifactFixture() model.ArtifactRef {
	return model.ArtifactRef{Kind: "audit", URI: "artifact://internal/private.log", SHA256: "artifact-hash"}
}

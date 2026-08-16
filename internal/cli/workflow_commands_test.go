package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/eventstore"
)

func TestWorkflowCommandsSupportJSONFromNestedProject(t *testing.T) {
	root := initializeCleanGitWorkflowProject(t)
	nested := filepath.Join(root, "src", "service")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	plan := executeJSON(t, cli.ExitOK,
		"plan", "create",
		"--project", nested,
		"--title", "Offline cache",
		"--task", "Implement cache",
		"--acceptance", "passes offline test",
		"--constraint", "no network",
		"--actor", "USR-LEAD",
		"--role", "lead",
		"--json",
	)
	requirement := requireObject(t, plan, "requirement")
	requirementID := requireString(t, requirement, "id")
	tasks := requireArray(t, plan, "tasks")
	if len(tasks) != 1 {
		t.Fatalf("tasks count = %d, want 1", len(tasks))
	}
	taskID := requireString(t, requireMap(t, tasks[0]), "id")

	approval := executeJSON(t, cli.ExitOK,
		"--project", nested, "--json",
		"approve", requirementID,
		"--actor", "USR-LEAD", "--role", "lead",
	)
	if got := requireString(t, approval, "requirement_id"); got != requirementID {
		t.Fatalf("approved requirement = %q, want %q", got, requirementID)
	}

	started := executeJSON(t, cli.ExitOK,
		"run", "start", taskID,
		"--project", nested,
		"--executor", "codex",
		"--actor", "AGT-001",
		"--actor-kind", "agent",
		"--json",
	)
	runID := requireString(t, started, "id")

	finished := executeJSON(t, cli.ExitOK,
		"run", "finish", runID,
		"--project", nested,
		"--result", "implemented",
		"--actor", "AGT-001",
		"--actor-kind", "agent",
		"--json",
	)
	if got := requireString(t, finished, "run_id"); got != runID {
		t.Fatalf("finished run = %q, want %q", got, runID)
	}

	gate := executeJSON(t, cli.ExitGate,
		"complete", taskID,
		"--project", nested,
		"--actor", "USR-REVIEWER",
		"--role", "reviewer",
		"--json",
	)
	if got := requireNumber(t, gate, "code"); got != cli.ExitGate {
		t.Fatalf("gate code = %d, want %d", got, cli.ExitGate)
	}

	evidencePath := filepath.Join(t.TempDir(), "offline-test.log")
	evidenceContent := []byte("PASS: offline cache\n")
	if err := os.WriteFile(evidencePath, evidenceContent, 0o644); err != nil {
		t.Fatal(err)
	}
	wantDigestBytes := sha256.Sum256(evidenceContent)
	wantDigest := hex.EncodeToString(wantDigestBytes[:])
	evidence := executeJSON(t, cli.ExitOK,
		"verify", taskID,
		"--project", nested,
		"--kind", "test",
		"--evidence", evidencePath,
		"--outcome", "pass",
		"--actor", "USR-REVIEWER",
		"--role", "reviewer",
		"--json",
	)
	if got := requireString(t, evidence, "sha256"); got != wantDigest {
		t.Fatalf("evidence digest = %q, want %q", got, wantDigest)
	}

	completed := executeJSON(t, cli.ExitOK,
		"complete", taskID,
		"--project", nested,
		"--actor", "USR-REVIEWER",
		"--role", "reviewer",
		"--json",
	)
	if got := requireString(t, completed, "task_id"); got != taskID {
		t.Fatalf("completed task = %q, want %q", got, taskID)
	}

	status := executeJSON(t, cli.ExitOK, "status", "--project", nested, "--json")
	statusTasks := requireObject(t, status, "tasks")
	if got := requireString(t, requireMap(t, statusTasks[taskID]), "status"); got != "Completed" {
		t.Fatalf("task status = %q, want Completed", got)
	}

	history := executeJSON(t, cli.ExitOK, "history", requirementID, "--project", nested, "--json")
	events := requireArray(t, history, "events")
	if len(events) != 2 {
		t.Fatalf("requirement history count = %d, want 2", len(events))
	}
}

func TestGateAgentApprovalReturnsExitApproval(t *testing.T) {
	root := initializeWorkflowProject(t)
	plan := executeJSON(t, cli.ExitOK,
		"--project", root, "--json",
		"plan", "create",
		"--title", "Govern changes",
		"--task", "Implement gate",
		"--acceptance", "tests pass",
		"--actor", "USR-LEAD",
		"--role", "lead",
	)
	requirementID := requireString(t, requireObject(t, plan, "requirement"), "id")

	result := executeJSON(t, cli.ExitApproval,
		"approve", requirementID,
		"--project", root,
		"--actor", "AGT-APPROVER",
		"--role", "agent",
		"--json",
	)
	if got := requireNumber(t, result, "code"); got != cli.ExitApproval {
		t.Fatalf("approval code = %d, want %d", got, cli.ExitApproval)
	}
}

func TestRecordCommandsScanAndAttributeCurrentGitChange(t *testing.T) {
	root := initializeGitWorkflowProject(t)
	writeWorkflowFile(t, root, "api.go", "before")
	gitWorkflow(t, root, "add", ".gitignore", "api.go")
	gitWorkflow(t, root, "commit", "-m", "baseline")
	writeWorkflowFile(t, root, "api.go", "after")

	plan := executeJSON(t, cli.ExitOK,
		"plan", "create",
		"--project", root,
		"--title", "Govern changes",
		"--task", "Implement scan",
		"--acceptance", "records current changes",
		"--actor", "USR-OWNER",
		"--role", "owner",
		"--json",
	)
	taskID := requireString(t, requireMap(t, requireArray(t, plan, "tasks")[0]), "id")

	scan := executeJSON(t, cli.ExitOK,
		"record", "scan",
		"--project", root,
		"--actor", "USR-OWNER",
		"--role", "owner",
		"--json",
	)
	changes := requireArray(t, scan, "changes")
	if len(changes) != 1 {
		t.Fatalf("scanned changes = %#v, want one modified file", changes)
	}
	change := requireMap(t, changes[0])
	if got := requireString(t, change, "path"); got != "api.go" {
		t.Fatalf("scan path = %q, want api.go", got)
	}
	sha256 := requireString(t, change, "sha256")

	attribute := executeJSON(t, cli.ExitOK,
		"record", "attribute", "api.go",
		"--project", root,
		"--sha256", sha256,
		"--task", taskID,
		"--actor", "USR-OWNER",
		"--role", "owner",
		"--json",
	)
	if got := requireString(t, attribute, "path"); got != "api.go" {
		t.Fatalf("attributed path = %q, want api.go", got)
	}

	status := executeJSON(t, cli.ExitOK, "status", "--project", root, "--json")
	current := requireObject(t, status, "changes")
	if attributed, ok := requireMap(t, current["api.go"])["attributed"].(bool); !ok || !attributed {
		t.Fatalf("api.go attribution = %#v, want true", current["api.go"])
	}
}

func TestDirectCLIVerifyRejectsDirtyUnattributedChangeWithoutAppendingEvidence(t *testing.T) {
	root := initializeGitWorkflowProject(t)
	writeWorkflowFile(t, root, "api.go", "before")
	gitWorkflow(t, root, "add", ".gitignore", "api.go")
	gitWorkflow(t, root, "commit", "-m", "baseline")

	plan := executeJSON(t, cli.ExitOK,
		"plan", "create",
		"--project", root,
		"--title", "Govern changes",
		"--task", "Implement gate",
		"--acceptance", "rejects unattributed changes",
		"--actor", "USR-OWNER",
		"--role", "owner",
		"--json",
	)
	requirementID := requireString(t, requireObject(t, plan, "requirement"), "id")
	taskID := requireString(t, requireMap(t, requireArray(t, plan, "tasks")[0]), "id")
	executeJSON(t, cli.ExitOK,
		"approve", requirementID,
		"--project", root,
		"--actor", "USR-OWNER",
		"--role", "owner",
		"--json",
	)
	started := executeJSON(t, cli.ExitOK,
		"run", "start", taskID,
		"--project", root,
		"--executor", "codex",
		"--actor", "AGT-001",
		"--actor-kind", "agent",
		"--json",
	)
	executeJSON(t, cli.ExitOK,
		"run", "finish", requireString(t, started, "id"),
		"--project", root,
		"--result", "implemented",
		"--actor", "AGT-001",
		"--actor-kind", "agent",
		"--json",
	)

	before, err := eventstore.New(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeWorkflowFile(t, root, "api.go", "after")
	evidencePath := filepath.Join(t.TempDir(), "verify.log")
	if err := os.WriteFile(evidencePath, []byte("PASS\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	executeJSON(t, cli.ExitGate,
		"verify", taskID,
		"--project", root,
		"--kind", "test",
		"--evidence", evidencePath,
		"--outcome", "pass",
		"--actor", "USR-REVIEWER",
		"--role", "reviewer",
		"--json",
	)

	after, err := eventstore.New(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count after rejected verification = %d, want %d", len(after), len(before))
	}
}

func TestGateUnapprovedRunReturnsExitConflict(t *testing.T) {
	root := initializeWorkflowProject(t)
	plan := executeJSON(t, cli.ExitOK,
		"plan", "create",
		"--project", root,
		"--title", "Govern changes",
		"--task", "Implement gate",
		"--acceptance", "tests pass",
		"--actor", "USR-LEAD",
		"--role", "lead",
		"--json",
	)
	taskID := requireString(t, requireMap(t, requireArray(t, plan, "tasks")[0]), "id")

	result := executeJSON(t, cli.ExitConflict,
		"run", "start", taskID,
		"--project", root,
		"--executor", "codex",
		"--actor", "AGT-001",
		"--actor-kind", "agent",
		"--json",
	)
	if got := requireNumber(t, result, "code"); got != cli.ExitConflict {
		t.Fatalf("conflict code = %d, want %d", got, cli.ExitConflict)
	}
}

func TestWorkflowPlanRejectsMismatchedTaskAcceptancePairs(t *testing.T) {
	root := initializeWorkflowProject(t)
	result := executeJSON(t, cli.ExitUsage,
		"plan", "create",
		"--project", root,
		"--title", "Broken input",
		"--task", "first",
		"--task", "second",
		"--acceptance", "only first",
		"--actor", "USR-LEAD",
		"--role", "lead",
		"--json",
	)
	if got := requireNumber(t, result, "code"); got != cli.ExitUsage {
		t.Fatalf("usage code = %d, want %d", got, cli.ExitUsage)
	}
}

func TestJSONMissingEventLogReturnsExitFailure(t *testing.T) {
	root := initializeWorkflowProject(t)
	if err := os.Remove(filepath.Join(root, ".haowork", "events.jsonl")); err != nil {
		t.Fatal(err)
	}

	result := executeJSON(t, cli.ExitFailure, "status", "--project", root, "--json")
	if got := requireNumber(t, result, "code"); got != cli.ExitFailure {
		t.Fatalf("operational error code = %d, want %d", got, cli.ExitFailure)
	}
	if got := requireString(t, result, "error"); got == "" {
		t.Fatal("operational error message is empty")
	}
}

func TestJSONOutputFailureDoesNotAppendSecondDocument(t *testing.T) {
	root := initializeWorkflowProject(t)
	stdout := &failingWriter{limit: 8}
	var stderr bytes.Buffer

	code := cli.Execute(
		context.Background(),
		[]string{"status", "--project", root, "--json"},
		stdout,
		&stderr,
	)

	if code != cli.ExitFailure {
		t.Fatalf("Execute() code = %d, want %d", code, cli.ExitFailure)
	}
	if stdout.calls != 1 {
		t.Fatalf("stdout writes = %d, want one delivery attempt", stdout.calls)
	}
}

type failingWriter struct {
	buffer bytes.Buffer
	limit  int
	calls  int
}

func (w *failingWriter) Write(data []byte) (int, error) {
	w.calls++
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("injected output failure")
	}
	if len(data) > remaining {
		written, _ := w.buffer.Write(data[:remaining])
		return written, errors.New("injected output failure")
	}
	return w.buffer.Write(data)
}

func initializeWorkflowProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	result := executeJSON(t, cli.ExitOK,
		"init",
		"--project", root,
		"--name", "demo",
		"--actor", "USR-OWNER",
		"--goal", "Keep work auditable",
		"--done-when", "verified work completes",
		"--invariant", "no silent drift",
		"--project-id", "PRJ-TEST",
		"--json",
	)
	if got := requireString(t, result, "ProjectID"); got != "PRJ-TEST" {
		t.Fatalf("project id = %q, want PRJ-TEST", got)
	}
	return root
}

func initializeGitWorkflowProject(t *testing.T) string {
	t.Helper()
	root := initializeWorkflowProject(t)
	gitWorkflow(t, root, "init")
	gitWorkflow(t, root, "config", "user.email", "test@example.com")
	gitWorkflow(t, root, "config", "user.name", "Test User")
	writeWorkflowFile(t, root, ".gitignore", ".haowork/\n")
	return root
}

func initializeCleanGitWorkflowProject(t *testing.T) string {
	t.Helper()
	root := initializeGitWorkflowProject(t)
	gitWorkflow(t, root, "add", ".gitignore")
	gitWorkflow(t, root, "commit", "-m", "baseline")
	return root
}

func writeWorkflowFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitWorkflow(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func executeJSON(t *testing.T, wantCode int, args ...string) map[string]any {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute(context.Background(), args, &stdout, &stderr)
	if code != wantCode {
		t.Fatalf("Execute(%q) code = %d, want %d; stdout = %q; stderr = %q", args, code, wantCode, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Execute(%q) stderr = %q, want no JSON-mode diagnostics", args, stderr.String())
	}

	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("Execute(%q) stdout = %q, want JSON object: %v", args, stdout.String(), err)
	}
	if result == nil {
		t.Fatalf("Execute(%q) stdout = %q, want JSON object", args, stdout.String())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("Execute(%q) stdout = %q, want exactly one JSON document", args, stdout.String())
	}
	return result
}

func requireObject(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	return requireMap(t, object[key])
}

func requireMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want JSON object", value)
	}
	return result
}

func requireArray(t *testing.T, object map[string]any, key string) []any {
	t.Helper()
	result, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want JSON array", key, object[key])
	}
	return result
}

func requireString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	result, ok := object[key].(string)
	if !ok || result == "" {
		t.Fatalf("%s = %#v, want non-empty string", key, object[key])
	}
	return result
}

func requireNumber(t *testing.T, object map[string]any, key string) int {
	t.Helper()
	result, ok := object[key].(float64)
	if !ok {
		t.Fatalf("%s = %#v, want number", key, object[key])
	}
	return int(result)
}

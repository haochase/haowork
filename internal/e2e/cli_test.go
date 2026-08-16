package e2e_test

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
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2
	exitConflict = 3
	exitGate     = 4
	exitApproval = 5
)

type commandResult struct {
	code   int
	stdout string
	stderr string
}

func TestGovernedCLIWorkflowRecoversAndRejectsTampering(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join(mustWorkingDirectory(t), "..", ".."))
	binaryName := "haowork"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 2*time.Minute)
	build := runProcess(buildContext, repositoryRoot, "go", "build", "-trimpath", "-o", binaryPath, "./cmd/haowork")
	cancelBuild()
	requireCommand(t, build, exitOK)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	projectRoot := t.TempDir()
	runCLI := func(args ...string) commandResult {
		t.Helper()
		return runProcess(ctx, repositoryRoot, binaryPath, args...)
	}
	runJSON := func(wantCode int, args ...string) map[string]any {
		t.Helper()
		result := runCLI(args...)
		requireCommand(t, result, wantCode)
		if result.stderr != "" {
			t.Fatalf("haowork %q stderr = %q, want empty JSON diagnostics", args, result.stderr)
		}
		return decodeJSONObject(t, result.stdout)
	}

	t.Run("1 init requires and preserves GoalVersion v1", func(t *testing.T) {
		missingCriterion := runCLI(
			"init", "--project", projectRoot,
			"--name", "recovery-demo",
			"--actor", "USR-OWNER",
			"--goal", "Keep governed work auditable",
			"--json",
		)
		requireCommand(t, missingCriterion, exitUsage)

		runJSON(exitOK,
			"init", "--project", projectRoot,
			"--name", "recovery-demo",
			"--project-id", "PRJ-E2E",
			"--actor", "USR-OWNER",
			"--goal", "Keep governed work auditable",
			"--done-when", "the governed task is completed",
			"--done-when", "history remains replayable",
			"--invariant", "no silent goal drift",
			"--json",
		)

		status := runJSON(exitOK, "status", "--project", projectRoot, "--json")
		goal := requireObject(t, status, "goal")
		if got := requireNumber(t, goal, "version"); got != 1 {
			t.Fatalf("goal version = %d, want 1", got)
		}
		if got := requireString(t, goal, "statement"); got != "Keep governed work auditable" {
			t.Fatalf("goal statement = %q", got)
		}
		criteria := requireArray(t, goal, "completion_criteria")
		wantCriteria := []any{"the governed task is completed", "history remains replayable"}
		if !reflect.DeepEqual(criteria, wantCriteria) {
			t.Fatalf("completion criteria = %#v, want %#v", criteria, wantCriteria)
		}
	})
	makeProjectGitClean(t, projectRoot)

	plan := runJSON(exitOK,
		"plan", "create", "--project", projectRoot,
		"--title", "Prove recovery",
		"--task", "Exercise public workflow",
		"--acceptance", "all recovery checks pass",
		"--constraint", "use only Capsule facts",
		"--actor", "USR-LEAD",
		"--role", "lead",
		"--json",
	)
	requirementID := requireString(t, requireObject(t, plan, "requirement"), "id")
	tasks := requireArray(t, plan, "tasks")
	if len(tasks) != 1 {
		t.Fatalf("planned tasks = %d, want 1", len(tasks))
	}
	taskID := requireString(t, requireMap(t, tasks[0]), "id")

	t.Run("2 Agent cannot approve", func(t *testing.T) {
		result := runJSON(exitApproval,
			"approve", requirementID, "--project", projectRoot,
			"--actor", "AGT-APPROVER", "--role", "agent", "--json",
		)
		if got := requireString(t, result, "error"); got != "human approval required" {
			t.Fatalf("approval error = %q", got)
		}
	})

	t.Run("3 human Lead can approve", func(t *testing.T) {
		result := runJSON(exitOK,
			"approve", requirementID, "--project", projectRoot,
			"--actor", "USR-LEAD", "--role", "lead", "--json",
		)
		if got := requireString(t, result, "requirement_id"); got != requirementID {
			t.Fatalf("approved requirement = %q, want %q", got, requirementID)
		}
	})

	var runID string
	t.Run("4 Run can start and finish", func(t *testing.T) {
		started := runJSON(exitOK,
			"run", "start", taskID, "--project", projectRoot,
			"--executor", "codex", "--actor", "AGT-WORKER", "--actor-kind", "agent", "--json",
		)
		runID = requireString(t, started, "id")
		if got := requireString(t, started, "status"); got != "Running" {
			t.Fatalf("started run status = %q, want Running", got)
		}

		finished := runJSON(exitOK,
			"run", "finish", runID, "--project", projectRoot,
			"--result", "workflow exercised", "--actor", "AGT-WORKER", "--actor-kind", "agent", "--json",
		)
		if got := requireString(t, finished, "run_id"); got != runID {
			t.Fatalf("finished run = %q, want %q", got, runID)
		}
	})

	t.Run("5 completion before evidence exits 4", func(t *testing.T) {
		result := runJSON(exitGate,
			"complete", taskID, "--project", projectRoot,
			"--actor", "USR-REVIEWER", "--role", "reviewer", "--json",
		)
		if got := requireNumber(t, result, "code"); got != exitGate {
			t.Fatalf("completion gate code = %d, want %d", got, exitGate)
		}
	})

	evidencePath := filepath.Join(t.TempDir(), "evidence.txt")
	evidenceContent := []byte("PASS: public workflow recovery\n")
	if err := os.WriteFile(evidencePath, evidenceContent, 0o644); err != nil {
		t.Fatal(err)
	}
	wantDigestBytes := sha256.Sum256(evidenceContent)
	wantDigest := hex.EncodeToString(wantDigestBytes[:])

	t.Run("6 passing evidence records its file hash", func(t *testing.T) {
		evidence := runJSON(exitOK,
			"verify", taskID, "--project", projectRoot,
			"--kind", "test", "--evidence", evidencePath, "--outcome", "pass",
			"--actor", "USR-REVIEWER", "--role", "reviewer", "--json",
		)
		if got := requireString(t, evidence, "sha256"); got != wantDigest {
			t.Fatalf("evidence hash = %q, want %q", got, wantDigest)
		}
	})

	t.Run("7 human Reviewer can complete", func(t *testing.T) {
		completed := runJSON(exitOK,
			"complete", taskID, "--project", projectRoot,
			"--actor", "USR-REVIEWER", "--role", "reviewer", "--json",
		)
		if got := requireString(t, completed, "task_id"); got != taskID {
			t.Fatalf("completed task = %q, want %q", got, taskID)
		}
	})

	var completedStatus map[string]any
	t.Run("8 JSON status shows Completed", func(t *testing.T) {
		completedStatus = runJSON(exitOK, "status", "--project", projectRoot, "--json")
		statusTask := requireMap(t, requireObject(t, completedStatus, "tasks")[taskID])
		if got := requireString(t, statusTask, "status"); got != "Completed" {
			t.Fatalf("task status = %q, want Completed", got)
		}
	})

	t.Run("9 deleting runtime does not change reconstructed status", func(t *testing.T) {
		runtimePath := filepath.Join(projectRoot, ".haowork", "runtime")
		if err := os.RemoveAll(runtimePath); err != nil {
			t.Fatal(err)
		}
		recoveredStatus := runJSON(exitOK, "status", "--project", projectRoot, "--json")
		if !reflect.DeepEqual(recoveredStatus, completedStatus) {
			t.Fatalf("recovered status changed\n got: %#v\nwant: %#v", recoveredStatus, completedStatus)
		}
	})

	eventsPath := filepath.Join(projectRoot, ".haowork", "events.jsonl")
	originalEvents, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("10 historical payload tampering fails as history corruption", func(t *testing.T) {
		tampered := bytes.Replace(originalEvents, []byte("Keep governed work auditable"), []byte("Keep governed work obscured"), 1)
		if bytes.Equal(tampered, originalEvents) {
			t.Fatal("historical payload replacement did not change events.jsonl")
		}
		if err := os.WriteFile(eventsPath, tampered, 0o644); err != nil {
			t.Fatal(err)
		}
		result := runCLI("status", "--project", projectRoot)
		requireCommand(t, result, exitFailure)
		if result.stdout != "" {
			t.Fatalf("tampered status stdout = %q, want empty", result.stdout)
		}
		if !strings.Contains(result.stderr, "event history is corrupt") {
			t.Fatalf("tampered status stderr = %q, want history-corrupt diagnostic", result.stderr)
		}
	})

	if err := os.WriteFile(eventsPath, originalEvents, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("11 manifest-only GoalVersion drift follows initialized event history", func(t *testing.T) {
		manifestPath := filepath.Join(projectRoot, ".haowork", "manifest.yaml")
		manifest, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		drifted := bytes.Replace(manifest, []byte("current_goal_version: 1"), []byte("current_goal_version: 2"), 1)
		if bytes.Equal(drifted, manifest) {
			t.Fatal("goal version replacement did not change manifest.yaml")
		}
		if err := os.WriteFile(manifestPath, drifted, 0o644); err != nil {
			t.Fatal(err)
		}
		result := runCLI("status", "--project", projectRoot)
		requireCommand(t, result, exitOK)
		if !strings.Contains(result.stdout, "goal v1") {
			t.Fatalf("goal-drift status stdout = %q, want replayed goal version", result.stdout)
		}
		if result.stderr != "" {
			t.Fatalf("goal-drift status stderr = %q, want empty", result.stderr)
		}
	})
}

func runProcess(ctx context.Context, directory, name string, args ...string) commandResult {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := exitOK
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			code = exitError.ExitCode()
		} else {
			code = -1
			stderr.WriteString(err.Error())
		}
	}
	return commandResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func requireCommand(t *testing.T, result commandResult, wantCode int) {
	t.Helper()
	if result.code != wantCode {
		t.Fatalf("exit code = %d, want %d; stdout = %q; stderr = %q", result.code, wantCode, result.stdout, result.stderr)
	}
}

func decodeJSONObject(t *testing.T, output string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("stdout = %q, want JSON object: %v", output, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout = %q, want exactly one JSON document", output)
	}
	return result
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func makeProjectGitClean(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test User"},
	} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".haowork/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", ".gitignore"}, {"commit", "-m", "baseline"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
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

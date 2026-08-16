package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/haochase/haowork/internal/testkit"
)

func TestRootHelpNamesTheControlPlane(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "external control plane") {
		t.Fatalf("help = %q, want product boundary", stdout.String())
	}
}

func TestTeamStatusWithoutRuntimeTokenUsesOfflineExitAndHidesToken(t *testing.T) {
	root := initializeStatusProject(t)
	if err := teamsync.SaveConfig(context.Background(), root, teamsync.ClientConfig{Endpoint: "http://127.0.0.1:8787", DeviceID: "DEV-TEST", TeamProjectID: "PRJ-TEST"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAOWORK_TEAM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{"--project", root, "--json", "team", "status"}, &stdout, &stderr)
	if code != cli.ExitOffline {
		t.Fatalf("team status code = %d, want %d; stdout = %q", code, cli.ExitOffline, stdout.String())
	}
	decodeOneJSONObject(t, stdout.String())
	if strings.Contains(stdout.String(), "HAOWORK_TEAM_TOKEN") || strings.Contains(stderr.String(), "HAOWORK_TEAM_TOKEN") {
		t.Fatalf("token source name leaked in command output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestUnknownCommandUsesArgumentExitCode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"unknown"}, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("Execute() code = %d, want %d", code, cli.ExitUsage)
	}
}

func TestRootJSONWritesOneObject(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"--json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	decodeOneJSONObject(t, stdout.String())
}

func TestRootJSONHelpWritesOneObject(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"--json", "--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	decodeOneJSONObject(t, stdout.String())
}

func TestUnknownCommandJSONWritesUsageError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"--json", "unknown"}, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("Execute() code = %d, want %d", code, cli.ExitUsage)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	result := decodeOneJSONObject(t, stdout.String())
	if result["code"] != float64(cli.ExitUsage) {
		t.Fatalf("JSON code = %#v, want %d", result["code"], cli.ExitUsage)
	}
	message, ok := result["error"].(string)
	if !ok || message == "" {
		t.Fatalf("JSON error = %#v, want non-empty string", result["error"])
	}
}

func TestInitRequiresGoalAndCompletionCriteria(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{
		"init", "--project", root, "--name", "demo", "--actor", "USR-ALICE", "--goal", "ship it",
	}, &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("Execute() code = %d, want %d; stderr = %q", code, cli.ExitUsage, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork")); !os.IsNotExist(err) {
		t.Fatalf("capsule exists after invalid init: %v", err)
	}
}

func TestProjectCommandsHonorPersistentProjectAndJSONFlags(t *testing.T) {
	root := t.TempDir()
	var initOut bytes.Buffer
	var initErr bytes.Buffer

	initCode := cli.Execute(context.Background(), []string{
		"init", "--project", root, "--json", "--name", "demo", "--actor", "USR-ALICE",
		"--goal", "ship it", "--done-when", "tests pass", "--invariant", "offline first", "--project-id", "PRJ-TEST",
	}, &initOut, &initErr)
	if initCode != cli.ExitOK {
		t.Fatalf("init code = %d, stderr = %q", initCode, initErr.String())
	}
	initResult := decodeOneJSONObject(t, initOut.String())
	if initResult["ProjectID"] != "PRJ-TEST" {
		t.Fatalf("init project id = %#v, want %q", initResult["ProjectID"], "PRJ-TEST")
	}
	events, err := eventstore.New(root).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "project.initialized" {
		t.Fatalf("init events = %#v, want one project.initialized event", events)
	}

	nested := filepath.Join(root, "src", "service")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	var statusOut bytes.Buffer
	var statusErr bytes.Buffer
	statusCode := cli.Execute(context.Background(), []string{"status", "--project", nested, "--json"}, &statusOut, &statusErr)
	if statusCode != cli.ExitOK {
		t.Fatalf("status code = %d, stderr = %q", statusCode, statusErr.String())
	}
	statusResult := decodeOneJSONObject(t, statusOut.String())
	if statusResult["project_id"] != "PRJ-TEST" {
		t.Fatalf("status project id = %#v, want %q", statusResult["project_id"], "PRJ-TEST")
	}
}

func TestProjectCommandsHonorPersistentFlagsBeforeSubcommand(t *testing.T) {
	root := t.TempDir()
	var initOut bytes.Buffer
	var initErr bytes.Buffer

	initCode := cli.Execute(context.Background(), []string{
		"--project", root, "--json", "init", "--name", "demo", "--actor", "USR-ALICE",
		"--goal", "ship it", "--done-when", "tests pass", "--project-id", "PRJ-TEST",
	}, &initOut, &initErr)
	if initCode != cli.ExitOK {
		t.Fatalf("init code = %d, stderr = %q", initCode, initErr.String())
	}
	decodeOneJSONObject(t, initOut.String())

	var statusOut bytes.Buffer
	var statusErr bytes.Buffer
	statusCode := cli.Execute(context.Background(), []string{"--project", root, "--json", "status"}, &statusOut, &statusErr)
	if statusCode != cli.ExitOK {
		t.Fatalf("status code = %d, stderr = %q", statusCode, statusErr.String())
	}
	statusResult := decodeOneJSONObject(t, statusOut.String())
	if statusResult["project_id"] != "PRJ-TEST" {
		t.Fatalf("status project id = %#v, want %q", statusResult["project_id"], "PRJ-TEST")
	}
}

func TestStatusJSONMatchesSharedProjectRuntime(t *testing.T) {
	root := t.TempDir()
	var initOut bytes.Buffer
	var initErr bytes.Buffer

	initCode := cli.Execute(context.Background(), []string{
		"init", "--project", root, "--json", "--name", "demo", "--actor", "USR-ALICE",
		"--goal", "ship it", "--done-when", "tests pass", "--project-id", "PRJ-TEST",
	}, &initOut, &initErr)
	if initCode != cli.ExitOK {
		t.Fatalf("init code = %d, stderr = %q", initCode, initErr.String())
	}

	nested := filepath.Join(root, "src", "service")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := core.Open(context.Background(), nested, core.Dependencies{
		IDs:   &testkit.IDs{},
		Clock: testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeStatus, err := project.Service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var statusOut bytes.Buffer
	var statusErr bytes.Buffer
	statusCode := cli.Execute(context.Background(), []string{"status", "--project", nested, "--json"}, &statusOut, &statusErr)
	if statusCode != cli.ExitOK {
		t.Fatalf("status code = %d, stderr = %q", statusCode, statusErr.String())
	}
	statusResult := decodeOneJSONObject(t, statusOut.String())
	if got := statusResult["project_id"]; got != runtimeStatus.ProjectID {
		t.Fatalf("status project_id = %#v, want %q", got, runtimeStatus.ProjectID)
	}
	goal, ok := statusResult["goal"].(map[string]any)
	if !ok {
		t.Fatalf("status goal = %#v, want JSON object", statusResult["goal"])
	}
	if got := goal["version"]; got != float64(runtimeStatus.Goal.Version) {
		t.Fatalf("status goal version = %#v, want %d", got, runtimeStatus.Goal.Version)
	}
}

func TestStatusJSONPreservesEventStoreErrorText(t *testing.T) {
	tests := []struct {
		name          string
		breakEventLog func(*testing.T, string)
	}{
		{
			name: "missing event log",
			breakEventLog: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, ".haowork", "events.jsonl")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt event log",
			breakEventLog: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, ".haowork", "events.jsonl"), []byte("{\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := initializeStatusProject(t)
			test.breakEventLog(t, root)
			_, wantErr := eventstore.New(root).ReadAll(context.Background())
			if wantErr == nil {
				t.Fatal("eventstore.ReadAll() error = nil, want broken event log error")
			}

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := cli.Execute(context.Background(), []string{"status", "--project", root, "--json"}, &stdout, &stderr)
			if code != cli.ExitFailure {
				t.Fatalf("status code = %d, want %d; stderr = %q", code, cli.ExitFailure, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("status stderr = %q, want empty JSON-mode diagnostics", stderr.String())
			}
			result := decodeOneJSONObject(t, stdout.String())
			if got := result["code"]; got != float64(cli.ExitFailure) {
				t.Fatalf("status code = %#v, want %d", got, cli.ExitFailure)
			}
			if got := result["error"]; got != wantErr.Error() {
				t.Fatalf("status error = %#v, want %q", got, wantErr.Error())
			}
		})
	}
}

func initializeStatusProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{
		"init", "--project", root, "--json", "--name", "demo", "--actor", "USR-ALICE",
		"--goal", "ship it", "--done-when", "tests pass", "--project-id", "PRJ-TEST",
	}, &stdout, &stderr)
	if code != cli.ExitOK {
		t.Fatalf("init code = %d, stderr = %q", code, stderr.String())
	}
	decodeOneJSONObject(t, stdout.String())
	return root
}

func TestInitHelpDescribesDurableGoalInputs(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"init", "--help"}, &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	want := map[string]string{
		"--goal":      "GoalVersion v1 statement",
		"--done-when": "GoalVersion v1 completion criterion",
		"--invariant": "GoalVersion v1 invariant",
	}
	for flag, text := range want {
		if !hasHelpLine(stdout.String(), flag, text) {
			t.Fatalf("help = %q, want durable goal contract for %s", stdout.String(), flag)
		}
	}
}

func hasHelpLine(help, flag, text string) bool {
	for _, line := range strings.Split(help, "\n") {
		if strings.Contains(line, flag) && strings.Contains(line, text) {
			return true
		}
	}
	return false
}

func decodeOneJSONObject(t *testing.T, output string) map[string]any {
	t.Helper()

	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(output))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("stdout = %q, want JSON object: %v", output, err)
	}
	if result == nil {
		t.Fatalf("stdout = %q, want JSON object", output)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("stdout = %q, want one JSON object", output)
	}
	return result
}

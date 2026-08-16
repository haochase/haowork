package cli_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/haochase/haowork/internal/cli"
)

func TestContextCLIShowMatchesBuildJSON(t *testing.T) {
	root := initializeWorkflowProject(t)
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := executeJSON(t, cli.ExitOK, "plan", "create", "--project", root, "--title", "Build context", "--task", "Prepare", "--acceptance", "context available", "--actor", "USR-OWNER", "--role", "owner", "--json")
	requirementID := requireString(t, requireObject(t, plan, "requirement"), "id")
	tasks := requireArray(t, plan, "tasks")
	taskID := requireString(t, requireMap(t, tasks[0]), "id")
	executeJSON(t, cli.ExitOK, "approve", requirementID, "--project", root, "--actor", "USR-OWNER", "--role", "owner", "--json")
	built := executeJSON(t, cli.ExitOK, "context", "build", taskID, "--project", root, "--source", "brief.txt", "--reason", "prepare", "--actor", "USR-OWNER", "--role", "owner", "--json")
	contextID := requireString(t, built, "id")
	shown := executeJSON(t, cli.ExitOK, "context", "show", contextID, "--project", root, "--json")
	if !reflect.DeepEqual(built, shown) {
		t.Fatalf("context JSON differs: built=%#v shown=%#v", built, shown)
	}
}

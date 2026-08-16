package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/cli"
)

func TestAgentTeamsCLICommandsAreDeterministic(t *testing.T) {
	for _, args := range [][]string{{"mission", "status"}, {"agents", "status"}, {"skills", "list"}, {"trace", "list"}, {"approvals", "list"}, {"transfer", "preview"}} {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(context.Background(), append([]string{"--json", "--project", t.TempDir()}, args...), &stdout, &stderr)
		if code == cli.ExitUsage && strings.Contains(stdout.String(), "unknown command") {
			t.Fatalf("%v returned unknown-command usage error: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
		if strings.Contains(strings.ToLower(stdout.String()+stderr.String()), "access_token") {
			t.Fatalf("%v leaked credential field", args)
		}
	}
}

func TestMissionIssueRejectsMissingWorkerAssignmentsBeforeAPI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{"--json", "mission", "issue", "--task", "TSK-1", "--context", "CTX-1", "--actor", "owner"}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Fatalf("code = %d, want usage; stdout=%q", code, stdout.String())
	}
}

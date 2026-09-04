package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

func TestTransferReturnCommandIsAvailableBeforeOpeningLocalCore(t *testing.T) {
	input := filepath.Join(t.TempDir(), "return-request.json")
	if err := os.WriteFile(input, []byte(`{"approval":{"id":"APR-001"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{"--json", "transfer", "return", "--input", input, "--output", filepath.Join(t.TempDir(), "return.zip")}, &stdout, &stderr)
	if code != cli.ExitOffline {
		t.Fatalf("return command exit=%d, want offline Core; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "unknown command") {
		t.Fatalf("return command is not registered: %q", stderr.String())
	}
}

func TestTransferReturnApprovalRequestCommandIsAvailableBeforeOpeningLocalCore(t *testing.T) {
	input := filepath.Join(t.TempDir(), "return-request.json")
	if err := os.WriteFile(input, []byte(`{"Base":{"TransferID":"XFR-001"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := cli.Execute(context.Background(), []string{"--json", "--project", t.TempDir(), "transfer", "request-return-approval", "--input", input, "--actor", "AGT-BUILD"}, &stdout, &stderr)
	if code != cli.ExitOffline {
		t.Fatalf("request-return-approval exit=%d, want offline Core; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "unknown command") {
		t.Fatalf("request-return-approval command is not registered: %q", stderr.String())
	}
}

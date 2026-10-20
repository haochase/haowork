package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTeamAIUnboundDoctorJSON(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"teamai", "doctor", "--project", root, "--json"}, &stdout, &stderr)
	var result struct {
		Configured bool   `json:"configured"`
		Status     string `json:"status"`
	}
	if code != ExitOK || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Configured || result.Status != "Unbound" {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("doctor wrote files: %v %v", entries, err)
	}
}

func TestTeamAIBindRequiresFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"teamai", "bind", "--project", t.TempDir(), "--json"}, &stdout, &stderr)
	if code != ExitUsage || !json.Valid(stdout.Bytes()) {
		t.Fatalf("code=%d stdout=%s", code, &stdout)
	}
}

func TestTeamAIBindRejectsEmptyRoleBeforeOpeningCore(t *testing.T) {
	for _, role := range []string{"", " \t\r\n "} {
		t.Run(role, func(t *testing.T) {
			root := t.TempDir()
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), []string{
				"teamai", "bind", "--project", root, "--json",
				"--team-repo", "https://github.com/example/team", "--revision", strings.Repeat("a", 40),
				"--actor", "USR-1", "--role", role,
			}, &stdout, &stderr)
			var result errorOutput
			if code != ExitUsage || json.Unmarshal(stdout.Bytes(), &result) != nil || result.Error != "--role must be owner or lead" {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected bind wrote files: %v %v", entries, err)
			}
		})
	}
}

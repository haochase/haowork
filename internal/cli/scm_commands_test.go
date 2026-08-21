package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSCMCommandsRequireArgumentsBeforeOpeningCore(t *testing.T) {
	for _, args := range [][]string{
		{"scm", "scan"},
		{"scm", "show"},
		{"scm", "propose"},
		{"scm", "confirm"},
		{"scm", "reject"},
		{"scm", "verify-history"},
	} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), append([]string{"--json"}, args...), &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("Execute(%v) code = %d, stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if strings.Count(strings.TrimSpace(stdout.String()), "\n") > 0 {
			t.Fatalf("Execute(%v) emitted multiple JSON lines: %q", args, stdout.String())
		}
	}
}

func TestSCMStatusRequiresRunningLocalCore(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--project", t.TempDir(), "--json", "scm", "status"}, &stdout, &stderr)
	if code != ExitFailure {
		t.Fatalf("code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "running local Core") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

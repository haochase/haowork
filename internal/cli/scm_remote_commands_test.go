package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestGitHubSCMCommandsValidateArgumentsBeforeOpeningCore(t *testing.T) {
	for _, args := range [][]string{{"scm", "github", "connect"}, {"scm", "github", "sync"}} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), append([]string{"--json"}, args...), &stdout, &stderr)
		if code != ExitUsage {
			t.Fatalf("Execute(%v) code = %d, stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestGitHubSCMStatusRequiresRunningLocalCore(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--project", t.TempDir(), "--json", "scm", "github", "status"}, &stdout, &stderr)
	if code != ExitFailure {
		t.Fatalf("code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "running local Core") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

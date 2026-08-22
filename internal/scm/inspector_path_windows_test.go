//go:build windows

package scm

import (
	"os/exec"
	"testing"
)

func createDirectoryAlias(t *testing.T, target, alias string) {
	t.Helper()
	output, err := exec.Command("cmd", "/c", "mklink", "/J", alias, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create directory junction: %v\n%s", err, output)
	}
}

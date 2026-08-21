package scm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestInspectorRegistersAndObservesExactCommit(t *testing.T) {
	root := newGitRepository(t)
	writeGitFile(t, root, "internal/app/service.go", "package app\n")
	runGit(t, root, "add", "internal/app/service.go")
	runGit(t, root, "commit", "-m", "add governed service")
	commitOID := runGit(t, root, "rev-parse", "HEAD")

	inspector := NewInspector()
	repository, err := inspector.Register(context.Background(), root, "PRJ-001")
	if err != nil {
		t.Fatal(err)
	}
	if repository.Provider != "local-git" || repository.ObjectFormat != "sha1" {
		t.Fatalf("repository = %#v", repository)
	}
	if repository.RemoteFingerprint == "" || strings.Contains(fmt.Sprintf("%#v", repository), "example.test") {
		t.Fatalf("repository leaked or omitted remote identity: %#v", repository)
	}

	observation, err := inspector.ObserveCommit(context.Background(), root, repository, commitOID)
	if err != nil {
		t.Fatal(err)
	}
	wantEmailDigest := sha256.Sum256([]byte("developer@example.test"))
	if observation.CommitOID != commitOID || observation.Message != "add governed service" {
		t.Fatalf("observation = %#v", observation)
	}
	if observation.AuthorEmailSHA256 != fmt.Sprintf("%x", wantEmailDigest) || strings.Contains(fmt.Sprintf("%#v", observation), "developer@example.test") {
		t.Fatalf("email was not reduced to a digest: %#v", observation)
	}
	if len(observation.Changes) != 1 || observation.Changes[0].Status != "added" || observation.Changes[0].Path != "internal/app/service.go" {
		t.Fatalf("changes = %#v", observation.Changes)
	}
}

func TestInspectorPreservesRenameAndChecksReachability(t *testing.T) {
	root := newGitRepository(t)
	writeGitFile(t, root, "old.txt", "governed\n")
	runGit(t, root, "add", "old.txt")
	runGit(t, root, "commit", "-m", "initial")
	runGit(t, root, "branch", "accepted")
	if err := os.Rename(filepath.Join(root, "old.txt"), filepath.Join(root, "new.txt")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "rename governed file")
	renameOID := runGit(t, root, "rev-parse", "HEAD")

	inspector := NewInspector()
	repository, err := inspector.Register(context.Background(), root, "PRJ-001")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := inspector.ObserveCommit(context.Background(), root, repository, renameOID)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Changes) != 1 || observation.Changes[0].Status != "renamed" || observation.Changes[0].PreviousPath != "old.txt" || observation.Changes[0].Path != "new.txt" {
		t.Fatalf("rename changes = %#v", observation.Changes)
	}
	reachable, err := inspector.IsReachable(context.Background(), root, renameOID, []string{"refs/heads/accepted"})
	if err != nil {
		t.Fatal(err)
	}
	if reachable {
		t.Fatal("unmerged commit reported reachable from accepted branch")
	}
	runGit(t, root, "branch", "-f", "accepted", renameOID)
	reachable, err = inspector.IsReachable(context.Background(), root, renameOID, []string{"refs/heads/accepted"})
	if err != nil || !reachable {
		t.Fatalf("reachable = %v, err = %v", reachable, err)
	}
}

func TestInspectorRejectsSymbolicOrOptionRevision(t *testing.T) {
	root := newGitRepository(t)
	inspector := NewInspector()
	repository, err := inspector.Register(context.Background(), root, "PRJ-001")
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range []string{"HEAD", "--help", "main^{commit}"} {
		if _, err := inspector.ObserveCommit(context.Background(), root, repository, revision); err == nil {
			t.Fatalf("ObserveCommit(%q) succeeded", revision)
		}
	}
}

func TestCommandRunnerRejectsCommandsOutsideReadOnlyAllowlist(t *testing.T) {
	root := newGitRepository(t)
	runner := CommandRunner{}
	for _, args := range [][]string{{"commit"}, {"config", "user.name", "Attacker"}, {"-c", "alias.x=commit", "x"}, {"remote", "set-url", "origin", "https://attacker.test/repo"}} {
		if _, err := runner.Run(context.Background(), root, args...); err == nil {
			t.Fatalf("Run(%q) succeeded", args)
		}
	}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Developer")
	runGit(t, root, "config", "user.email", "developer@example.test")
	runGit(t, root, "remote", "add", "origin", "https://token:secret@example.test/acme/demo.git?credential=private")
	return root
}

func writeGitFile(t *testing.T, root, name, contents string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

var _ = model.SCMRepository{}

package changes

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestScannerReportsGitChangesWithContentHashesAndBaseline(t *testing.T) {
	root := newGitRepository(t)
	writeFile(t, root, "modified.txt", "before")
	writeFile(t, root, "deleted.txt", "delete me")
	writeFile(t, root, "rename-from.txt", "rename me")
	git(t, root, "add", ".")
	git(t, root, "commit", "-m", "baseline")
	baseline := git(t, root, "rev-parse", "HEAD")

	writeFile(t, root, "modified.txt", "after")
	writeFile(t, root, "untracked.txt", "untracked")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "mv", "rename-from.txt", "rename-to.txt")
	writeFile(t, root, ".haowork/runtime/ignored.txt", "runtime")
	writeFile(t, root, ".haowork/cache/ignored.txt", "cache")
	writeFile(t, root, ".haowork/index/ignored.txt", "index")

	changes, err := (Scanner{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]struct {
		status string
		sha    string
	}{
		"modified.txt":  {status: "modified", sha: sha256Text("after")},
		"untracked.txt": {status: "untracked", sha: sha256Text("untracked")},
		"deleted.txt":   {status: "deleted", sha: ""},
		"rename-to.txt": {status: "renamed", sha: sha256Text("rename me")},
	}
	if len(changes) != len(want) {
		t.Fatalf("change count = %d, want %d: %#v", len(changes), len(want), changes)
	}
	for _, change := range changes {
		expectation, exists := want[change.Path]
		if !exists {
			t.Fatalf("unexpected change %#v", change)
		}
		if change.Status != expectation.status {
			t.Fatalf("status for %q = %q, want %q", change.Path, change.Status, expectation.status)
		}
		if change.SHA256 != expectation.sha {
			t.Fatalf("SHA256 for %q = %q, want %q", change.Path, change.SHA256, expectation.sha)
		}
		if change.Baseline != baseline {
			t.Fatalf("Baseline for %q = %q, want %q", change.Path, change.Baseline, baseline)
		}
		if change.Attributed {
			t.Fatalf("Attributed for %q = true, want false", change.Path)
		}
		delete(want, change.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing changes: %#v", want)
	}
}

func TestScannerReturnsActionableErrorWhenRepositoryHasNoHead(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init")
	writeFile(t, root, "untracked.txt", "untracked")

	_, err := (Scanner{}).Scan(context.Background(), root)
	if err == nil {
		t.Fatal("Scan() error = nil, want actionable error for repository without HEAD")
	}
}

func TestScannerRejectsGitBaselineChangeDuringScan(t *testing.T) {
	headReads := 0
	scanner := Scanner{output: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args, "\x00") {
		case "rev-parse\x00HEAD":
			headReads++
			if headReads == 1 {
				return []byte("before\n"), nil
			}
			return []byte("after\n"), nil
		case "status\x00--porcelain=v1\x00-z\x00--untracked-files=all":
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected Git arguments %q", args)
		}
	}}

	_, err := scanner.Scan(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Git baseline changed") {
		t.Fatalf("Scan() error = %v, want retryable Git baseline change error", err)
	}
	if headReads != 2 {
		t.Fatalf("HEAD reads = %d, want start and end checks", headReads)
	}
}

func TestScannerPreservesSpecialCharacterPath(t *testing.T) {
	root := newGitRepository(t)
	writeFile(t, root, "tracked.txt", "baseline")
	git(t, root, "add", "tracked.txt")
	git(t, root, "commit", "-m", "baseline")

	path := "nested/path with spaces #1%.txt"
	writeFile(t, root, path, "untracked")
	changes, err := (Scanner{}).Scan(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != path || changes[0].Status != "untracked" {
		t.Fatalf("Scan() changes = %#v, want preserved special path", changes)
	}
}

func TestParsePorcelainV1ZRejectsTruncatedRename(t *testing.T) {
	_, err := parsePorcelainV1Z([]byte("R  renamed.txt\x00"))
	if err == nil || !strings.Contains(err.Error(), "missing its source path") {
		t.Fatalf("parsePorcelainV1Z() error = %v, want truncated rename rejection", err)
	}
}

func TestGitOutputBytesIncludesGitStderr(t *testing.T) {
	_, err := gitOutputBytes(context.Background(), t.TempDir(), "definitely-not-a-git-command")
	if err == nil || !strings.Contains(err.Error(), "definitely-not-a-git-command") {
		t.Fatalf("gitOutputBytes() error = %v, want actionable Git stderr", err)
	}
}

func TestSymlinkSetupOnlySkipsPermissionErrors(t *testing.T) {
	if symlinkPrivilegeUnavailable(errors.New("simulated symbolic-link failure")) {
		t.Fatal("non-permission symbolic-link failure must not skip the test")
	}
}

func symlinkPrivilegeUnavailable(err error) bool {
	return errors.Is(err, fs.ErrPermission) ||
		(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))) // ERROR_PRIVILEGE_NOT_HELD
}

func createSymbolicLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if symlinkPrivilegeUnavailable(err) {
			t.Skipf("symbolic-link creation lacks required permission or privilege: %v", err)
		}
		t.Fatal(err)
	}
}

func TestScannerRejectsChangedSymlinkOutsideWorkspace(t *testing.T) {
	root := newGitRepository(t)
	writeFile(t, root, "tracked.txt", "baseline")
	git(t, root, "add", "tracked.txt")
	git(t, root, "commit", "-m", "baseline")

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outside-link.txt")
	createSymbolicLink(t, outside, link)

	_, err := (Scanner{}).Scan(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Scan() error = %v, want actionable symbolic link rejection", err)
	}
}

func TestReadFileNoFollowRejectsSymbolicLink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "outside-link.txt")
	createSymbolicLink(t, outside, link)

	_, err := readFileNoFollow(link)
	if !errors.Is(err, errRefusingSymbolicLink) {
		t.Fatalf("readFileNoFollow() error = %v, want symbolic link rejection", err)
	}
}

func newGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test User")
	return root
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func sha256Text(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

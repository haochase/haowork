package changes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

// WorkspaceScanner reads the current Git working-tree changes for a project.
type WorkspaceScanner interface {
	Scan(context.Context, string) ([]model.FileChange, error)
}

// Scanner reads Git porcelain output without invoking a shell.
type Scanner struct {
	output func(context.Context, string, ...string) ([]byte, error)
	digest func(string) (string, error)
}

var errRefusingSymbolicLink = errors.New("refusing symbolic link")

func (s Scanner) Scan(ctx context.Context, root string) ([]model.FileChange, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("scan workspace: project root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("scan workspace %q: resolve project root: %w", root, err)
	}
	baseline, err := s.outputText(ctx, absRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("scan workspace %q: read Git baseline: %w", absRoot, err)
	}
	if baseline == "" {
		return nil, fmt.Errorf("scan workspace %q: Git baseline is empty", absRoot)
	}
	output, err := s.outputBytes(ctx, absRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("scan workspace %q: read Git working tree: %w", absRoot, err)
	}

	entries, err := parsePorcelainV1Z(output)
	if err != nil {
		return nil, fmt.Errorf("scan workspace %q: parse Git working tree: %w", absRoot, err)
	}
	changes := make([]model.FileChange, 0, len(entries))
	for _, entry := range entries {
		if excluded(entry.path) {
			continue
		}
		change := model.FileChange{
			Path:     entry.path,
			Status:   entry.status,
			Baseline: baseline,
		}
		if change.Status != "deleted" {
			digest, err := s.digestFile(filepath.Join(absRoot, filepath.FromSlash(change.Path)))
			if err != nil {
				return nil, fmt.Errorf("scan workspace %q: hash changed file %q: %w", absRoot, change.Path, err)
			}
			change.SHA256 = digest
		}
		changes = append(changes, change)
	}
	currentBaseline, err := s.outputText(ctx, absRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("scan workspace %q: confirm Git baseline: %w", absRoot, err)
	}
	if currentBaseline != baseline {
		return nil, fmt.Errorf("scan workspace %q: Git baseline changed during scan (%q -> %q); retry", absRoot, baseline, currentBaseline)
	}
	return changes, nil
}

type porcelainEntry struct {
	path   string
	status string
}

func parsePorcelainV1Z(output []byte) ([]porcelainEntry, error) {
	parts := strings.Split(string(output), "\x00")
	entries := make([]porcelainEntry, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		record := parts[index]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("invalid porcelain entry %q", record)
		}
		xy := record[:2]
		path := record[3:]
		if path == "" {
			return nil, errors.New("porcelain entry has an empty path")
		}
		if xy[0] == 'R' || xy[1] == 'R' || xy[0] == 'C' || xy[1] == 'C' {
			if index+1 >= len(parts) || parts[index+1] == "" {
				return nil, fmt.Errorf("rename porcelain entry %q is missing its source path", record)
			}
			index++
		}
		entries = append(entries, porcelainEntry{path: filepath.ToSlash(path), status: classifyStatus(xy)})
	}
	return entries, nil
}

func classifyStatus(xy string) string {
	if xy == "??" {
		return "untracked"
	}
	if strings.Contains(xy, "D") {
		return "deleted"
	}
	if strings.Contains(xy, "R") {
		return "renamed"
	}
	if strings.Contains(xy, "C") {
		return "copied"
	}
	return "modified"
}

func excluded(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	for _, prefix := range []string{".haowork/runtime", ".haowork/cache", ".haowork/index"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (s Scanner) outputText(ctx context.Context, root string, args ...string) (string, error) {
	output, err := s.outputBytes(ctx, root, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (s Scanner) outputBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	if s.output != nil {
		return s.output(ctx, root, args...)
	}
	return gitOutputBytes(ctx, root, args...)
}

func (s Scanner) digestFile(path string) (string, error) {
	if s.digest != nil {
		return s.digest(path)
	}
	return digestFile(path)
}

func gitOutputBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			diagnostic := strings.TrimSpace(string(exitError.Stderr))
			if diagnostic != "" {
				return nil, fmt.Errorf("%w: %s", err, diagnostic)
			}
		}
		return nil, err
	}
	return output, nil
}

func digestFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errRefusingSymbolicLink
	}
	contents, err := readFileNoFollow(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

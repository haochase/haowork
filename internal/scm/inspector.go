package scm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

const (
	defaultGitTimeout = 10 * time.Second
	defaultMaxOutput  = 4 << 20
)

type Runner interface {
	Run(ctx context.Context, root string, args ...string) ([]byte, error)
}

type CommandRunner struct {
	Timeout   time.Duration
	MaxOutput int
}

type GitError struct {
	Args     []string
	ExitCode int
	Message  string
}

func (e *GitError) Error() string {
	return fmt.Sprintf("git %s failed with exit code %d: %s", strings.Join(e.Args, " "), e.ExitCode, e.Message)
}

type Inspector struct {
	Runner Runner
	Clock  func() time.Time
}

func NewInspector() *Inspector {
	return &Inspector{
		Runner: CommandRunner{Timeout: defaultGitTimeout, MaxOutput: defaultMaxOutput},
		Clock:  time.Now,
	}
}

func (runner CommandRunner) Run(ctx context.Context, root string, args ...string) ([]byte, error) {
	if !allowedGitArguments(args) {
		return nil, errors.New("Git command is outside the read-only allowlist")
	}
	timeout := runner.Timeout
	if timeout <= 0 {
		timeout = defaultGitTimeout
	}
	maxOutput := runner.MaxOutput
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutput
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve Git root: %w", err)
	}
	if info, statErr := os.Stat(root); statErr != nil || !info.IsDir() {
		return nil, errors.New("Git root must be an existing directory")
	}

	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(commandContext, "git", commandArgs...)
	command.Env = safeGitEnvironment()
	stdout := &boundedBuffer{limit: maxOutput}
	stderr := &boundedBuffer{limit: maxOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if commandContext.Err() != nil {
			return nil, fmt.Errorf("Git command timed out: %w", commandContext.Err())
		}
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return nil, &GitError{Args: append([]string(nil), args...), ExitCode: exitCode, Message: strings.TrimSpace(stderr.String())}
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("Git command output exceeded the configured limit")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func (inspector *Inspector) Register(ctx context.Context, root, projectID string) (model.SCMRepository, error) {
	if inspector == nil || inspector.Runner == nil || inspector.Clock == nil {
		return model.SCMRepository{}, errors.New("SCM inspector is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return model.SCMRepository{}, errors.New("project ID is required")
	}
	rootOutput, err := inspector.Runner.Run(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return model.SCMRepository{}, fmt.Errorf("discover repository root: %w", err)
	}
	if !samePath(root, strings.TrimSpace(string(rootOutput))) {
		return model.SCMRepository{}, errors.New("SCM root must be the repository top level")
	}
	formatOutput, err := inspector.Runner.Run(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return model.SCMRepository{}, fmt.Errorf("discover object format: %w", err)
	}
	objectFormat := strings.TrimSpace(string(formatOutput))
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return model.SCMRepository{}, fmt.Errorf("unsupported Git object format %q", objectFormat)
	}

	remoteFingerprint, err := inspector.remoteFingerprint(ctx, root)
	if err != nil {
		return model.SCMRepository{}, err
	}
	identityInput := projectID + "\n" + objectFormat + "\n" + remoteFingerprint
	if remoteFingerprint == "" {
		absoluteRoot, absErr := filepath.Abs(root)
		if absErr != nil {
			return model.SCMRepository{}, absErr
		}
		identityInput += "\n" + strings.ToLower(filepath.Clean(absoluteRoot))
	}
	identityDigest := sha256.Sum256([]byte(identityInput))
	return model.SCMRepository{
		ID:                "SCM-" + hex.EncodeToString(identityDigest[:12]),
		ProjectID:         projectID,
		Provider:          "local-git",
		ObjectFormat:      objectFormat,
		RemoteFingerprint: remoteFingerprint,
		RegisteredAt:      inspector.Clock().UTC(),
	}, nil
}

func (inspector *Inspector) ObserveCommit(ctx context.Context, root string, repository model.SCMRepository, commitOID string) (model.CommitObservation, error) {
	if inspector == nil || inspector.Runner == nil {
		return model.CommitObservation{}, errors.New("SCM inspector is not configured")
	}
	oidLength, err := objectIDLength(repository.ObjectFormat)
	if err != nil {
		return model.CommitObservation{}, err
	}
	if !validObjectID(commitOID, oidLength) {
		return model.CommitObservation{}, errors.New("commit must be a complete lowercase object ID")
	}
	objectType, err := inspector.Runner.Run(ctx, root, "cat-file", "-t", commitOID)
	if err != nil {
		return model.CommitObservation{}, fmt.Errorf("verify commit object: %w", err)
	}
	if strings.TrimSpace(string(objectType)) != "commit" {
		return model.CommitObservation{}, errors.New("SCM object is not a commit")
	}
	format := "%H%x00%T%x00%P%x00%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI%x00%B"
	metadata, err := inspector.Runner.Run(ctx, root, "show", "-s", "--no-show-signature", "--format="+format, commitOID)
	if err != nil {
		return model.CommitObservation{}, fmt.Errorf("read commit metadata: %w", err)
	}
	fields := strings.SplitN(string(metadata), "\x00", 10)
	if len(fields) != 10 {
		return model.CommitObservation{}, errors.New("Git commit metadata response is incomplete")
	}
	resolvedOID := strings.TrimSpace(fields[0])
	if resolvedOID != commitOID {
		return model.CommitObservation{}, errors.New("Git resolved a different commit object")
	}
	authoredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[5]))
	if err != nil {
		return model.CommitObservation{}, fmt.Errorf("parse author time: %w", err)
	}
	committedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[8]))
	if err != nil {
		return model.CommitObservation{}, fmt.Errorf("parse committer time: %w", err)
	}
	changesOutput, err := inspector.Runner.Run(ctx, root, "diff-tree", "--root", "--first-parent", "--no-commit-id", "-r", "-M", "-C", "--raw", "-z", "--no-abbrev", commitOID)
	if err != nil {
		return model.CommitObservation{}, fmt.Errorf("read commit changes: %w", err)
	}
	changes, err := parseRawChanges(changesOutput, oidLength)
	if err != nil {
		return model.CommitObservation{}, err
	}
	parents := strings.Fields(strings.TrimSpace(fields[2]))
	for _, parent := range parents {
		if !validObjectID(parent, oidLength) {
			return model.CommitObservation{}, errors.New("Git returned an invalid parent object ID")
		}
	}
	return model.CommitObservation{
		RepositoryID:         repository.ID,
		CommitOID:            resolvedOID,
		TreeOID:              strings.TrimSpace(fields[1]),
		ParentOIDs:           parents,
		AuthorName:           strings.TrimSpace(fields[3]),
		AuthorEmailSHA256:    emailDigest(fields[4]),
		CommitterName:        strings.TrimSpace(fields[6]),
		CommitterEmailSHA256: emailDigest(fields[7]),
		AuthoredAt:           authoredAt.UTC(),
		CommittedAt:          committedAt.UTC(),
		Message:              strings.TrimSpace(fields[9]),
		Changes:              changes,
	}, nil
}

func (inspector *Inspector) IsReachable(ctx context.Context, root, commitOID string, refs []string) (bool, error) {
	if inspector == nil || inspector.Runner == nil {
		return false, errors.New("SCM inspector is not configured")
	}
	if !validObjectID(commitOID, 40) && !validObjectID(commitOID, 64) {
		return false, errors.New("commit must be a complete lowercase object ID")
	}
	if len(refs) == 0 {
		return false, errors.New("at least one accepted ref is required")
	}
	for _, ref := range refs {
		if !validAcceptedRef(ref) {
			return false, fmt.Errorf("accepted ref %q is invalid", ref)
		}
		_, err := inspector.Runner.Run(ctx, root, "merge-base", "--is-ancestor", commitOID, ref)
		if err == nil {
			return true, nil
		}
		var gitErr *GitError
		if !errors.As(err, &gitErr) || gitErr.ExitCode != 1 {
			return false, fmt.Errorf("check commit reachability: %w", err)
		}
	}
	return false, nil
}

func (inspector *Inspector) remoteFingerprint(ctx context.Context, root string) (string, error) {
	remotes, err := inspector.Runner.Run(ctx, root, "remote")
	if err != nil {
		return "", fmt.Errorf("list Git remotes: %w", err)
	}
	foundOrigin := false
	for _, remote := range strings.Fields(string(remotes)) {
		if remote == "origin" {
			foundOrigin = true
			break
		}
	}
	if !foundOrigin {
		return "", nil
	}
	urls, err := inspector.Runner.Run(ctx, root, "remote", "get-url", "--all", "origin")
	if err != nil {
		return "", fmt.Errorf("read origin identity: %w", err)
	}
	canonical := make([]string, 0)
	for _, value := range strings.Split(strings.TrimSpace(string(urls)), "\n") {
		value = sanitizeRemoteURL(strings.TrimSpace(value))
		if value != "" {
			canonical = append(canonical, value)
		}
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	return hex.EncodeToString(digest[:]), nil
}

func parseRawChanges(output []byte, oidLength int) ([]model.SCMFileChange, error) {
	if len(output) == 0 {
		return nil, nil
	}
	parts := bytes.Split(output, []byte{0})
	changes := make([]model.SCMFileChange, 0, len(parts)/2)
	for index := 0; index < len(parts); {
		if len(parts[index]) == 0 {
			index++
			continue
		}
		header := strings.Fields(string(parts[index]))
		index++
		if len(header) != 5 || !strings.HasPrefix(header[0], ":") {
			return nil, errors.New("Git raw change header is malformed")
		}
		if index >= len(parts) {
			return nil, errors.New("Git raw change path is missing")
		}
		statusToken := header[4]
		if statusToken == "" {
			return nil, errors.New("Git raw change status is missing")
		}
		change := model.SCMFileChange{OldBlobOID: nonZeroOID(header[2]), NewBlobOID: nonZeroOID(header[3])}
		switch statusToken[0] {
		case 'A':
			change.Status = "added"
			change.Path = string(parts[index])
			index++
		case 'D':
			change.Status = "deleted"
			change.Path = string(parts[index])
			index++
		case 'M':
			change.Status = "modified"
			change.Path = string(parts[index])
			index++
		case 'T':
			change.Status = "type_changed"
			change.Path = string(parts[index])
			index++
		case 'R', 'C':
			if index+1 >= len(parts) {
				return nil, errors.New("Git renamed or copied change path is missing")
			}
			if statusToken[0] == 'R' {
				change.Status = "renamed"
			} else {
				change.Status = "copied"
			}
			change.PreviousPath = string(parts[index])
			change.Path = string(parts[index+1])
			index += 2
		default:
			return nil, fmt.Errorf("unsupported Git raw status %q", statusToken)
		}
		if (change.OldBlobOID != "" && !validObjectID(change.OldBlobOID, oidLength)) || (change.NewBlobOID != "" && !validObjectID(change.NewBlobOID, oidLength)) {
			return nil, errors.New("Git raw change contains an invalid object ID")
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].PreviousPath < changes[j].PreviousPath
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func safeGitEnvironment() []string {
	blocked := []string{"GIT_CONFIG_", "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SSH", "GIT_SSH_COMMAND", "GIT_ASKPASS", "GIT_EXTERNAL_DIFF", "GIT_NO_REPLACE_OBJECTS", "LC_ALL"}
	environment := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		upperName := strings.ToUpper(name)
		denied := false
		for _, prefix := range blocked {
			if upperName == prefix || strings.HasPrefix(upperName, prefix) {
				denied = true
				break
			}
		}
		if !denied {
			environment = append(environment, entry)
		}
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=Never",
		"GIT_PAGER=cat",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_EXTERNAL_DIFF=",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
	)
}

func allowedGitArguments(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "rev-parse":
		return len(args) == 2 && (args[1] == "--show-toplevel" || args[1] == "--show-object-format")
	case "cat-file":
		return len(args) == 3 && args[1] == "-t" && (validObjectID(args[2], 40) || validObjectID(args[2], 64))
	case "show":
		return len(args) == 5 && args[1] == "-s" && args[2] == "--no-show-signature" && strings.HasPrefix(args[3], "--format=") && (validObjectID(args[4], 40) || validObjectID(args[4], 64))
	case "diff-tree":
		return len(args) == 11 && args[1] == "--root" && args[2] == "--first-parent" && args[3] == "--no-commit-id" && args[4] == "-r" && args[5] == "-M" && args[6] == "-C" && args[7] == "--raw" && args[8] == "-z" && args[9] == "--no-abbrev" && (validObjectID(args[10], 40) || validObjectID(args[10], 64))
	case "merge-base":
		return len(args) == 4 && args[1] == "--is-ancestor" && (validObjectID(args[2], 40) || validObjectID(args[2], 64)) && validAcceptedRef(args[3])
	case "remote":
		return len(args) == 1 || (len(args) == 4 && args[1] == "get-url" && args[2] == "--all" && args[3] == "origin")
	default:
		return false
	}
}

func sanitizeRemoteURL(value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return strings.TrimSuffix(parsed.String(), "/")
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	return strings.TrimSuffix(value, "/")
}

func samePath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftAbsolute), filepath.Clean(rightAbsolute))
}

func objectIDLength(format string) (int, error) {
	switch format {
	case "sha1":
		return 40, nil
	case "sha256":
		return 64, nil
	default:
		return 0, fmt.Errorf("unsupported Git object format %q", format)
	}
}

func validObjectID(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validAcceptedRef(value string) bool {
	validPrefix := strings.HasPrefix(value, "refs/heads/") || strings.HasPrefix(value, "refs/remotes/") || strings.HasPrefix(value, "refs/tags/")
	if !validPrefix || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("/._-", character) {
			continue
		}
		return false
	}
	return true
}

func nonZeroOID(value string) string {
	if strings.Trim(value, "0") == "" {
		return ""
	}
	return value
}

func emailDigest(value string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(digest[:])
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if buffer.Buffer.Len()+len(data) > buffer.limit {
		remaining := buffer.limit - buffer.Buffer.Len()
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(data[:remaining])
		}
		buffer.exceeded = true
		return len(data), nil
	}
	return buffer.Buffer.Write(data)
}

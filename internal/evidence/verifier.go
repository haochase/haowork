package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/model"
)

type verifier struct {
	state         StateProvider
	scanner       changes.WorkspaceScanner
	runner        CommandRunner
	workspaceRoot string

	mu               sync.Mutex
	verifiedSnapshot map[string]string
}

func NewVerifier(state StateProvider, scanner changes.WorkspaceScanner, runner CommandRunner, workspaceRoot string) EvidenceVerifier {
	return &verifier{
		state:            state,
		scanner:          scanner,
		runner:           runner,
		workspaceRoot:    workspaceRoot,
		verifiedSnapshot: make(map[string]string),
	}
}

func (v *verifier) Verify(ctx context.Context, candidate EvidenceCandidate) (EvidenceDecision, error) {
	decision := EvidenceDecision{Status: "rejected"}
	add := func(name, status, detail string) {
		decision.Checks = append(decision.Checks, model.EvidenceCheck{Name: name, Status: status, Detail: detail})
	}

	if v.state == nil || v.scanner == nil || v.runner == nil || strings.TrimSpace(v.workspaceRoot) == "" {
		return EvidenceDecision{}, fmt.Errorf("evidence verifier is not configured")
	}
	if strings.TrimSpace(candidate.TaskID) == "" || strings.TrimSpace(candidate.RunID) == "" || strings.TrimSpace(candidate.ContextID) == "" {
		add("binding", "fail", "task_id, run_id, and context_id are required")
		return decision, nil
	}
	state, err := v.state.Snapshot(ctx)
	if err != nil {
		return EvidenceDecision{}, fmt.Errorf("load current project state: %w", err)
	}
	if stale, detail := validateBindings(state, candidate); stale {
		add("binding", "stale", detail)
		decision.Status = "stale"
		return decision, nil
	}
	add("binding", "pass", "task, run, context, and goal version match current state")
	add("agent_outcome", "input", strings.TrimSpace(candidate.Outcome))

	workspace, err := v.scanner.Scan(ctx, v.workspaceRoot)
	if err != nil {
		return EvidenceDecision{}, fmt.Errorf("scan workspace: %w", err)
	}
	workspaceDigest, err := WorkspaceDigest(workspace)
	if err != nil {
		return EvidenceDecision{}, fmt.Errorf("digest workspace: %w", err)
	}
	key := evidenceKey(candidate)
	v.mu.Lock()
	previous, seen := v.verifiedSnapshot[key]
	v.mu.Unlock()
	if seen && previous != workspaceDigest {
		add("workspace_digest", "stale", "workspace changed since this evidence was verified")
		decision.Status = "stale"
		return decision, nil
	}
	add("workspace_digest", "pass", workspaceDigest)
	for _, change := range workspace {
		attribution, attributed := state.Attributions[change.Path+"\x00"+change.SHA256]
		if !attributed || (attribution.TaskID != candidate.TaskID && attribution.TaskID != "external-manual") {
			add("change_attribution", "fail", "current workspace change is not attributed")
			return decision, nil
		}
	}
	add("change_attribution", "pass", "all current workspace changes are attributed")

	actualDigest, err := digestEvidenceFile(v.workspaceRoot, candidate.URI)
	if err != nil {
		add("artifact_digest", "fail", err.Error())
		return decision, nil
	}
	if !strings.EqualFold(actualDigest, strings.TrimSpace(candidate.SHA256)) {
		add("artifact_digest", "fail", "candidate sha256 does not match evidence file")
		return decision, nil
	}
	add("artifact_digest", "pass", actualDigest)

	argv := strings.Fields(candidate.Command)
	if len(argv) == 0 {
		add("command", "fail", "an argv command is required for independent verification")
		return decision, nil
	}
	result, err := v.runner.Run(ctx, argv, v.workspaceRoot)
	if err != nil {
		add("command", "fail", err.Error())
		return decision, nil
	}
	if result.ExitCode != 0 {
		add("command", "fail", fmt.Sprintf("exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr)))
		return decision, nil
	}
	add("command", "pass", "exit code 0")

	v.mu.Lock()
	v.verifiedSnapshot[key] = workspaceDigest
	v.mu.Unlock()
	decision.Status = "verified"
	return decision, nil
}

func validateBindings(state model.ProjectState, candidate EvidenceCandidate) (bool, string) {
	task, exists := state.Tasks[candidate.TaskID]
	if !exists {
		return true, "task is no longer current"
	}
	if task.GoalVersion != state.Goal.Version || task.LastRunID != candidate.RunID {
		return true, "task goal version or current run changed"
	}
	run, exists := state.Runs[candidate.RunID]
	if !exists || run.TaskID != candidate.TaskID || run.Status != model.StatusFinished {
		return true, "run is missing, belongs to another task, or is unfinished"
	}
	if run.GoalVersion != state.Goal.Version || run.ContextID != candidate.ContextID {
		return true, "run goal version or context changed"
	}
	slice, exists := state.Contexts[candidate.ContextID]
	if !exists || slice.TaskID != candidate.TaskID || slice.GoalVersion != state.Goal.Version || slice.Superseded {
		return true, "context is missing, stale, or superseded"
	}
	if run.ContextHash != slice.SliceHash {
		return true, "run context hash no longer matches the context slice"
	}
	return false, ""
}

func digestEvidenceFile(workspaceRoot, uri string) (string, error) {
	if strings.TrimSpace(uri) == "" {
		return "", fmt.Errorf("evidence uri is required")
	}
	path := uri
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRoot, path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read evidence file: %w", err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

// WorkspaceDigest is deterministic across scanner ordering and intentionally excludes attribution metadata.
func WorkspaceDigest(changes []model.FileChange) (string, error) {
	canonical := append([]model.FileChange(nil), changes...)
	for index := range canonical {
		canonical[index].Attributed = false
	}
	sort.Slice(canonical, func(left, right int) bool {
		return canonical[left].Path+"\x00"+canonical[left].SHA256 < canonical[right].Path+"\x00"+canonical[right].SHA256
	})
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func evidenceKey(candidate EvidenceCandidate) string {
	return strings.Join([]string{candidate.TaskID, candidate.RunID, candidate.ContextID, candidate.URI, candidate.SHA256}, "\x00")
}

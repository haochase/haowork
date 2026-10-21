package teamai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
)

type Detector struct {
	Runner CommandRunner
	FS     fs.FS
	// ProjectRoot is the workspace where official TeamAI initialization is
	// checked. It is independent of the root used for local asset verification.
	ProjectRoot string
}

func (d Detector) Doctor(ctx context.Context, root string, cfg Config) (DoctorResult, error) {
	result := DoctorResult{TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Assets: []AssetResult{}, Checks: []DoctorCheck{}, Errors: []Issue{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := cfg.Validate(); err != nil {
		result.Errors = append(result.Errors, issueFrom(err))
		return result, err
	}
	runner := d.Runner
	if runner == nil {
		commandRoot := d.ProjectRoot
		if commandRoot == "" {
			commandRoot = root
		}
		runner = ExecRunner{Dir: commandRoot}
	}
	output, err := runner.Run(ctx, "teamai", "--version")
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil || strings.TrimSpace(string(output)) == "" {
		issue := Issue{Code: "teamai_cli_unavailable", Message: "TeamAI CLI version is unavailable"}
		result.Errors = append(result.Errors, issue)
	} else {
		result.Installed = true
		version := strings.TrimSpace(string(output))
		if len(version) <= 80 && !strings.ContainsAny(version, "/\\\r\n:") {
			result.Version = version
		}
		doctorOutput, doctorErr := runner.Run(ctx, "teamai", "doctor", "--json")
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		var official struct {
			OK     bool          `json:"ok"`
			Scope  *string       `json:"scope"`
			Checks []DoctorCheck `json:"checks"`
		}
		if json.Unmarshal(doctorOutput, &official) != nil {
			result.Errors = append(result.Errors, Issue{Code: "teamai_doctor_invalid", Message: "TeamAI doctor did not return valid JSON"})
		} else {
			if official.Scope != nil && (*official.Scope == "project" || *official.Scope == "user") {
				result.Scope = official.Scope
			}
			checksOK := len(official.Checks) > 0
			for index, check := range official.Checks {
				checksOK = checksOK && check.OK
				// Official detail strings may contain paths or command output. Only
				// retain a bounded plain check label; redact other labels.
				if !safeDoctorLabel(check.Name) {
					check.Name = "check " + fmt.Sprint(index+1)
				}
				result.Checks = append(result.Checks, check)
			}
			result.Initialized = doctorErr == nil && official.OK && checksOK && result.Scope != nil && *result.Scope == cfg.Scope
			if !result.Initialized {
				result.Errors = append(result.Errors, Issue{Code: "teamai_not_initialized", Message: "TeamAI doctor did not confirm initialization in the configured scope"})
			}
		}
	}
	files := d.FS
	if files == nil {
		if err := rejectProtectedPath(root); err != nil {
			result.Errors = append(result.Errors, issueFrom(err))
			return result, joinIssues(result.Errors)
		}
		directory, openErr := os.OpenRoot(root)
		if openErr != nil {
			issue := Issue{Code: "teamai_asset_root_unavailable", Message: "asset root is unavailable"}
			result.Errors = append(result.Errors, issue)
			return result, joinIssues(result.Errors)
		}
		defer directory.Close()
		files = directory.FS()
	}
	verified, verifyErr := verifyFS(ctx, files, cfg)
	result.Assets = verified.Assets
	result.Errors = append(result.Errors, verified.Errors...)
	if errors.Is(verifyErr, context.Canceled) || errors.Is(verifyErr, context.DeadlineExceeded) {
		return result, verifyErr
	}
	return result, joinIssues(result.Errors)
}

// ExecRunner invokes the exact executable and arguments without a command shell.
type ExecRunner struct{ Dir string }

func (r ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = r.Dir
	return command.Output()
}

func safeDoctorLabel(label string) bool {
	if len(label) == 0 || len(label) > 100 {
		return false
	}
	for _, char := range label {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune(" -_()", char)) {
			return false
		}
	}
	return true
}

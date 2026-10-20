package teamai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Verify reads only listed assets beneath assetRoot. Relative asset roots are
// resolved beneath projectRoot; an empty root means the project directory.
// TeamRevision is the configured binding, while SHA-256 proves local file identity.
func Verify(ctx context.Context, projectRoot string, cfg Config, assetRoot string) (VerifyResult, error) {
	result := VerifyResult{Config: cfg, Assets: []AssetResult{}, Errors: []Issue{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := cfg.Validate(); err != nil {
		result.Errors = append(result.Errors, issueFrom(err))
		return result, err
	}
	if !filepath.IsAbs(assetRoot) {
		assetRoot = filepath.Join(projectRoot, assetRoot)
	}
	if err := rejectProtectedPath(assetRoot); err != nil {
		result.Errors = append(result.Errors, issueFrom(err))
		return result, err
	}
	directory, err := os.OpenRoot(assetRoot)
	if err != nil {
		issue := Issue{Code: "teamai_asset_root_unavailable", Message: "asset root is unavailable"}
		result.Errors = append(result.Errors, issue)
		return result, issue
	}
	defer directory.Close()
	return verifyFS(ctx, directory.FS(), cfg)
}

func verifyFS(ctx context.Context, files fs.FS, cfg Config) (VerifyResult, error) {
	result := VerifyResult{Config: cfg, Assets: []AssetResult{}, Errors: []Issue{}}
	for _, spec := range cfg.RequiredAssets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		asset := AssetResult{Path: spec.Path, Kind: spec.Kind}
		issue := inspectAsset(files, spec, &asset)
		result.Assets = append(result.Assets, asset)
		if issue != nil {
			result.Errors = append(result.Errors, *issue)
		}
	}
	result.Ready = len(result.Errors) == 0
	return result, joinIssues(result.Errors)
}

func inspectAsset(files fs.FS, spec AssetSpec, result *AssetResult) *Issue {
	parts := strings.Split(spec.Path, "/")
	for i := range parts {
		info, err := fs.Lstat(files, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return &Issue{Code: "teamai_asset_missing", Path: spec.Path, Message: "required asset is unavailable"}
		}
		if info.Mode()&fs.ModeSymlink != 0 || i == len(parts)-1 && !info.Mode().IsRegular() || i < len(parts)-1 && !info.IsDir() {
			return &Issue{Code: "teamai_asset_unsafe", Path: spec.Path, Message: "assets must be regular files without symbolic links"}
		}
	}
	data, err := fs.ReadFile(files, spec.Path)
	if err != nil {
		return &Issue{Code: "teamai_asset_missing", Path: spec.Path, Message: "required asset is unavailable"}
	}
	if spec.Kind == "skill" && !validSkill(data, path.Base(path.Dir(spec.Path))) {
		return &Issue{Code: "teamai_skill_invalid", Path: spec.Path, Message: "Skill frontmatter must contain its matching name and description"}
	}
	sum := sha256.Sum256(data)
	result.SHA256 = hex.EncodeToString(sum[:])
	if !strings.EqualFold(result.SHA256, spec.SHA256) {
		return &Issue{Code: "teamai_asset_hash_mismatch", Path: spec.Path, Message: "asset SHA-256 does not match the bound digest"}
	}
	result.Valid = true
	return nil
}

func validSkill(data []byte, expectedName string) bool {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return false
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] != "---" {
			continue
		}
		var metadata struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &metadata) != nil {
			return false
		}
		return metadata.Name == expectedName && strings.TrimSpace(metadata.Description) != ""
	}
	return false
}

func issueFrom(err error) Issue {
	var issue Issue
	if errors.As(err, &issue) {
		return issue
	}
	return configError("invalid TeamAI configuration")
}

func joinIssues(issues []Issue) error {
	errs := make([]error, len(issues))
	for i, issue := range issues {
		errs[i] = issue
	}
	return errors.Join(errs...)
}

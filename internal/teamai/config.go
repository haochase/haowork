package teamai

import (
	"bytes"
	"encoding/hex"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const governedSkillPath = "skills/haowork-governed-coding/SKILL.md"

var scpRepo = regexp.MustCompile(`^git@[a-zA-Z0-9][a-zA-Z0-9.-]*:[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

func LoadConfig(filename string) (Config, error) {
	if err := rejectProtectedPath(filename); err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, Issue{Code: "teamai_config_unavailable", Message: "TeamAI project configuration is unavailable"}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, configError("TeamAI project configuration must be valid YAML with known fields")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, configError("TeamAI project configuration must contain one YAML document")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func rejectProtectedPath(filename string) error {
	candidate := filename
	for pass := 0; pass < 2; pass++ {
		normalized := strings.ReplaceAll(candidate, "\\", "/")
		withoutDrive := normalized
		if len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/' &&
			(normalized[0] >= 'A' && normalized[0] <= 'Z' || normalized[0] >= 'a' && normalized[0] <= 'z') {
			withoutDrive = normalized[2:]
		}
		if strings.ContainsRune(withoutDrive, ':') {
			return Issue{Code: "teamai_path_unsafe", Message: "alternate data streams and invalid colon syntax must not be read"}
		}
		for _, segment := range strings.Split(normalized, "/") {
			segment = strings.ToLower(strings.TrimRight(segment, ". "))
			if segment == ".git" || segment == ".env" || strings.HasPrefix(segment, ".env.") || strings.Contains(segment, "secret") {
				return Issue{Code: "teamai_path_unsafe", Message: "protected metadata or credential paths must not be read"}
			}
		}
		if pass == 0 {
			candidate = resolvedPath(filename)
		}
	}
	return nil
}

func resolvedPath(filename string) string {
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return filename
	}
	return resolved
}

func (cfg Config) Validate() error {
	if cfg.SchemaVersion != "1" {
		return configError("schema_version must be 1")
	}
	if !validRepo(cfg.TeamRepo) {
		return configError("team_repo must be a credential-free HTTPS or SSH Git URL")
	}
	if !validHex(cfg.TeamRevision, 40, 64) {
		return configError("team_revision must be a pinned 40 or 64 character hexadecimal commit digest")
	}
	if cfg.Scope != "project" && cfg.Scope != "user" {
		return configError("scope must be project or user")
	}
	if cfg.AutoUpdate != "manual" {
		return configError("auto_update must be manual")
	}
	if len(cfg.RequiredAssets) == 0 {
		return configError("required_assets must not be empty")
	}
	seen := make(map[string]bool)
	hasGovernedSkill := false
	for _, asset := range cfg.RequiredAssets {
		if !safeAssetPath(asset.Path) {
			return Issue{Code: "teamai_asset_unsafe", Message: "asset path is outside the allowed public asset paths"}
		}
		if seen[strings.ToLower(asset.Path)] {
			return configError("required_assets contains duplicate paths")
		}
		seen[strings.ToLower(asset.Path)] = true
		if !validAssetKind(asset) {
			return configError("asset kind does not match its allowed directory")
		}
		if !validHex(asset.SHA256, 64) {
			return configError("each required asset must have a SHA-256 digest")
		}
		hasGovernedSkill = hasGovernedSkill || asset.Path == governedSkillPath && asset.Kind == "skill"
	}
	if !hasGovernedSkill {
		return Issue{Code: "teamai_governed_skill_required", Message: "haowork-governed-coding Skill is required"}
	}
	return nil
}

func configError(message string) Issue {
	return Issue{Code: "teamai_config_invalid", Message: message}
}

func validHex(value string, lengths ...int) bool {
	for _, length := range lengths {
		if len(value) == length {
			_, err := hex.DecodeString(value)
			return err == nil
		}
	}
	return false
}

func validRepo(repo string) bool {
	if repo == "" || strings.TrimSpace(repo) != repo || strings.ContainsAny(repo, "\r\n\t ") {
		return false
	}
	if scpRepo.MatchString(repo) {
		return validRepoPath(strings.SplitN(repo, ":", 2)[1])
	}
	u, err := url.Parse(repo)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || !validRepoPath(u.Path) {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.User == nil
	case "ssh":
		if u.User == nil {
			return true
		}
		_, password := u.User.Password()
		return u.User.Username() == "git" && !password
	}
	return false
}

func validRepoPath(value string) bool {
	value = strings.TrimPrefix(value, "/")
	return fs.ValidPath(value) && value != "." && !strings.ContainsAny(value, "\\:")
}

func safeAssetPath(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(name), "/") {
		if strings.HasPrefix(segment, ".") || strings.Contains(segment, "secret") || strings.HasSuffix(segment, ".") || strings.TrimSpace(segment) != segment {
			return false
		}
	}
	if name == "teamai.yaml" {
		return true
	}
	switch strings.SplitN(name, "/", 2)[0] {
	case "skills", "rules", "agents", "hooks", "mcp", "models", "learnings", "teamwiki", "docs":
		return strings.Contains(name, "/")
	}
	return false
}

func validAssetKind(asset AssetSpec) bool {
	if asset.Path == "teamai.yaml" {
		return asset.Kind == "config"
	}
	kinds := map[string]string{"skills": "skill", "rules": "rule", "agents": "agent", "hooks": "hook", "mcp": "mcp", "models": "model", "learnings": "learning", "teamwiki": "teamwiki", "docs": "doc"}
	if kinds[strings.SplitN(asset.Path, "/", 2)[0]] != asset.Kind {
		return false
	}
	return asset.Kind != "skill" || path.Base(asset.Path) == "SKILL.md"
}

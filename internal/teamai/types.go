// Package teamai verifies explicitly bound TeamAI assets without synchronizing them.
package teamai

import "context"

type Config struct {
	SchemaVersion  string      `json:"schema_version" yaml:"schema_version"`
	TeamRepo       string      `json:"team_repo" yaml:"team_repo"`
	TeamRevision   string      `json:"team_revision" yaml:"team_revision"`
	RequiredAssets []AssetSpec `json:"required_assets" yaml:"required_assets"`
	Scope          string      `json:"scope" yaml:"scope"`
	AutoUpdate     string      `json:"auto_update" yaml:"auto_update"`
}

type AssetSpec struct {
	Path   string `json:"path" yaml:"path"`
	Kind   string `json:"kind" yaml:"kind"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

// Issue exposes stable error codes without leaking command output or file content.
type Issue struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func (i Issue) Error() string { return i.Code + ": " + i.Message }

type AssetResult struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
	Valid  bool   `json:"valid"`
}

type DoctorResult struct {
	Installed    bool          `json:"installed"`
	Initialized  bool          `json:"initialized"`
	Scope        *string       `json:"scope"`
	Checks       []DoctorCheck `json:"checks"`
	Version      string        `json:"version"`
	TeamRepo     string        `json:"team_repo"`
	TeamRevision string        `json:"team_revision"`
	Assets       []AssetResult `json:"assets"`
	Errors       []Issue       `json:"errors"`
}

type DoctorCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

type VerifyResult struct {
	Config Config        `json:"config"`
	Assets []AssetResult `json:"assets"`
	Errors []Issue       `json:"errors"`
	Ready  bool          `json:"ready"`
}

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

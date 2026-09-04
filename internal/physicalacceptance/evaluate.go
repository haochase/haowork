package physicalacceptance

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

const (
	StatusBlocked          = "BLOCKED_PHYSICAL_ENVIRONMENT"
	StatusSSHAdminE2E      = "SSH_ADMIN_CHANNEL_E2E"
	StatusUSBPathReplay    = "USB_PATH_REPLAY"
	StatusPhysicalVerified = "PHYSICAL_DUAL_ZONE_VERIFIED"
)

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Evidence struct {
	SchemaVersion int             `json:"schema_version"`
	TransferID    string          `json:"transfer_id"`
	Transport     string          `json:"transport"`
	Stages        []StageEvidence `json:"stages"`
	Network       NetworkEvidence `json:"network"`
}

type StageEvidence struct {
	Name                string `json:"name"`
	SourceEnvironmentID string `json:"source_environment_id"`
	TargetEnvironmentID string `json:"target_environment_id"`
	ArtifactSHA256      string `json:"artifact_sha256"`
	Verified            bool   `json:"verified"`
	Transport           string `json:"transport"`
}

type NetworkEvidence struct {
	PublicToInternalBlocked bool `json:"public_to_internal_blocked"`
	InternalToPublicBlocked bool `json:"internal_to_public_blocked"`
	PublicCoreHealthy       bool `json:"public_core_healthy"`
	InternalCoreHealthy     bool `json:"internal_core_healthy"`
}

type Result struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

func Load(reader io.Reader) (Evidence, error) {
	if reader == nil {
		return Evidence{}, errors.New("physical evidence reader is required")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err := decoder.Decode(&evidence); err != nil {
		return Evidence{}, errors.New("physical evidence JSON is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Evidence{}, errors.New("physical evidence must contain one JSON object")
	}
	return evidence, nil
}

func Evaluate(evidence Evidence) Result {
	reasons := validate(evidence)
	if len(reasons) > 0 {
		return Result{Status: StatusBlocked, Reasons: reasons}
	}
	switch evidence.Transport {
	case "direct-usb":
		return Result{Status: StatusPhysicalVerified}
	case "ssh-admin-channel":
		return Result{Status: StatusSSHAdminE2E}
	case "usb-path-replay":
		return Result{Status: StatusUSBPathReplay}
	default:
		return Result{Status: StatusBlocked, Reasons: []string{"transport is not an accepted physical evidence mode"}}
	}
}

func validate(evidence Evidence) []string {
	reasons := make([]string, 0, 6)
	if evidence.SchemaVersion != 1 || strings.TrimSpace(evidence.TransferID) == "" {
		reasons = append(reasons, "evidence schema or transfer ID is invalid")
	}
	if evidence.Transport != "direct-usb" && evidence.Transport != "ssh-admin-channel" && evidence.Transport != "usb-path-replay" {
		reasons = append(reasons, "transport is not an accepted physical evidence mode")
	}
	if len(evidence.Stages) != 4 {
		reasons = append(reasons, "evidence must contain all four handoff stages")
	} else {
		expected := map[string][2]string{
			"public-export":   {"public", "internal"},
			"internal-import": {"public", "internal"},
			"internal-return": {"internal", "public"},
			"public-merge":    {"internal", "public"},
		}
		seen := make(map[string]struct{}, len(evidence.Stages))
		for _, stage := range evidence.Stages {
			pair, ok := expected[stage.Name]
			if !ok || pair[0] != strings.ToLower(strings.TrimSpace(stage.SourceEnvironmentID)) || pair[1] != strings.ToLower(strings.TrimSpace(stage.TargetEnvironmentID)) {
				reasons = append(reasons, "handoff stage direction is invalid")
			}
			if _, duplicate := seen[stage.Name]; duplicate {
				reasons = append(reasons, "handoff stage is duplicated")
			}
			seen[stage.Name] = struct{}{}
			if !stage.Verified || !sha256Pattern.MatchString(strings.ToLower(strings.TrimSpace(stage.ArtifactSHA256))) {
				reasons = append(reasons, "handoff stage verification or artifact SHA-256 is invalid")
			}
			if stage.Transport != evidence.Transport {
				reasons = append(reasons, "handoff stage transport does not match evidence transport")
			}
		}
	}
	if !evidence.Network.PublicToInternalBlocked || !evidence.Network.InternalToPublicBlocked {
		reasons = append(reasons, "business network is not blocked in both directions")
	}
	if !evidence.Network.PublicCoreHealthy || !evidence.Network.InternalCoreHealthy {
		reasons = append(reasons, "both zone Core health checks are required")
	}
	return uniqueReasons(reasons)
}

func uniqueReasons(reasons []string) []string {
	seen := make(map[string]struct{}, len(reasons))
	result := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		result = append(result, reason)
	}
	return result
}

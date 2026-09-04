package transferhost

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

const maxConfigBytes = 1 << 20

type hostConfig struct {
	Version             int                `json:"version"`
	EnvironmentID       string             `json:"environment_id"`
	ReturnTTLSeconds    int                `json:"return_ttl_seconds,omitempty"`
	SigningKey          signingKeyConfig   `json:"signing_key"`
	TrustedPublicKeys   []trustedKeyConfig `json:"trusted_public_keys"`
	RuntimeBindingsFile string             `json:"runtime_bindings_file"`
	ProvenanceFile      string             `json:"provenance_file"`
	Expected            expectedConfig     `json:"expected"`
}

type signingKeyConfig struct {
	KeyID          string `json:"key_id"`
	PrivateKeyFile string `json:"private_key_file"`
}

type trustedKeyConfig struct {
	KeyID         string `json:"key_id"`
	PublicKeyFile string `json:"public_key_file"`
}

type expectedConfig struct {
	GoalVersion    int               `json:"goal_version"`
	GitBaseline    string            `json:"git_baseline"`
	ContextHash    string            `json:"context_hash"`
	LeaseID        string            `json:"lease_id"`
	Scope          []string          `json:"scope"`
	RequiredSkills map[string]string `json:"required_skills"`
}

type bindingDocument struct {
	Version  int              `json:"version"`
	Bindings []runtimeBinding `json:"bindings"`
}

type runtimeBinding struct {
	LogicalActorID       string              `json:"logical_actor_id"`
	AgentFunction        model.AgentFunction `json:"agent_function"`
	Revision             int                 `json:"revision"`
	EnvironmentID        string              `json:"environment_id"`
	AgentTeamsInstanceID string              `json:"agentteams_instance_id"`
	RuntimePrincipalID   string              `json:"runtime_principal_id"`
	HumanPrincipalID     string              `json:"human_principal_id,omitempty"`
	LeaderRoomID         string              `json:"leader_room_id,omitempty"`
	TeamRoomID           string              `json:"team_room_id,omitempty"`
	Status               string              `json:"status"`
}

type provenanceDocument struct {
	Version int               `json:"version"`
	Entries []provenanceEntry `json:"entries"`
}

type provenanceEntry struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func decodeOwnerOnlyJSON(path string, target any) error {
	if err := ValidateOwnerOnlyFile(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxConfigBytes {
		return errors.New("trusted transfer configuration exceeds the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("trusted transfer configuration is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trusted transfer configuration must contain one JSON object")
	}
	return nil
}

func resolveConfigPath(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("trusted transfer file path is required")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("relative trusted transfer path escapes its configuration directory")
	}
	return filepath.Join(base, clean), nil
}

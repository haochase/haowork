package capsule

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

const ProtocolVersion = "0.1.0"

var (
	ErrNotFound      = errors.New("haowork project not found")
	ErrAlreadyExists = errors.New("capsule already exists")
)

type Manifest struct {
	ProtocolVersion    string    `yaml:"protocol_version"`
	ProjectID          string    `yaml:"project_id"`
	Name               string    `yaml:"name"`
	CurrentGoalVersion int       `yaml:"current_goal_version"`
	CreatedAt          time.Time `yaml:"created_at"`
	CreatedBy          string    `yaml:"created_by"`
}

type InitInput struct {
	ProjectID string
	Name      string
	ActorID   string
	CreatedAt time.Time
}

func Init(root string, input InitInput) (Manifest, error) {
	if input.ProjectID == "" || input.Name == "" || input.ActorID == "" {
		return Manifest{}, errors.New("project id, name, and actor id are required")
	}
	if input.CreatedAt.IsZero() {
		return Manifest{}, errors.New("created at is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Manifest{}, err
	}
	finalDir := filepath.Join(root, ".haowork")
	if _, err := os.Stat(finalDir); err == nil {
		return Manifest{}, fmt.Errorf("%w at %s", ErrAlreadyExists, finalDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	tempDir, err := os.MkdirTemp(root, ".haowork-init-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tempDir)
	for _, name := range []string{"design", "summary", "records", "identities", "evidence", "transfers", "runtime"} {
		if err := os.Mkdir(filepath.Join(tempDir, name), 0o755); err != nil {
			return Manifest{}, err
		}
	}
	manifest := Manifest{
		ProtocolVersion:    ProtocolVersion,
		ProjectID:          input.ProjectID,
		Name:               input.Name,
		CurrentGoalVersion: 1,
		CreatedAt:          input.CreatedAt.UTC(),
		CreatedBy:          input.ActorID,
	}
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(tempDir, "manifest.yaml"), data, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(tempDir, "events.jsonl"), nil, 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Find(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, ".haowork", "manifest.yaml")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", ErrNotFound
		}
		current = parent
	}
}

func Load(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ".haowork", "manifest.yaml"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("manifest contains multiple YAML documents")
		}
		return Manifest{}, err
	}
	if manifest.ProtocolVersion != ProtocolVersion {
		return Manifest{}, fmt.Errorf("unsupported protocol version %q", manifest.ProtocolVersion)
	}
	if manifest.ProjectID == "" || manifest.Name == "" || manifest.CurrentGoalVersion < 1 || manifest.CreatedBy == "" || manifest.CreatedAt.IsZero() {
		return Manifest{}, errors.New("manifest is missing required fields")
	}
	return manifest, nil
}

// UpdateGoalVersion advances the portable manifest by exactly one accepted
// goal version. Repeating a completed update is safe for recovery retries.
func UpdateGoalVersion(root string, expected, next int) error {
	if next != expected+1 {
		return fmt.Errorf("goal version must advance from %d to %d", expected, expected+1)
	}
	manifest, err := Load(root)
	if err != nil {
		return err
	}
	if manifest.CurrentGoalVersion == next {
		return nil
	}
	if manifest.CurrentGoalVersion != expected {
		return fmt.Errorf("manifest current goal version %d does not match expected %d", manifest.CurrentGoalVersion, expected)
	}
	manifest.CurrentGoalVersion = next
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	directory := filepath.Join(root, ".haowork")
	temporary, err := os.CreateTemp(directory, "manifest-goal-*.yaml")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(directory, "manifest.yaml"))
}

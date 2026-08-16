package teamsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const maxConfigBytes = 64 * 1024

// ClientConfig intentionally contains no credential material. Local Core
// obtains a bearer token from its runtime-only credential source.
type ClientConfig struct {
	Endpoint      string `json:"endpoint"`
	DeviceID      string `json:"device_id"`
	EnvironmentID string `json:"environment_id"`
	PrincipalID   string `json:"principal_id"`
	TeamProjectID string `json:"team_project_id"`
	Cursor        uint64 `json:"cursor"`
}

func configPath(root, deviceID string) string {
	return filepath.Join(root, ".haowork", "local", deviceID, "team.json")
}

func deviceLockPath(root, deviceID string) string {
	return filepath.Join(root, ".haowork", "local", deviceID, "device.lock")
}

func SaveConfig(ctx context.Context, root string, config ClientConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(config.DeviceID) == "" {
		return errors.New("device id is required")
	}
	return withDeviceLock(ctx, root, config.DeviceID, false, func() error {
		encoded, err := json.Marshal(config)
		if err != nil {
			return err
		}
		if len(encoded) > maxConfigBytes {
			return errors.New("team config exceeds size limit")
		}
		temporary, err := os.CreateTemp(filepath.Dir(configPath(root, config.DeviceID)), "team-*.json")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if _, err := temporary.Write(encoded); err != nil {
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
		return os.Rename(temporaryPath, configPath(root, config.DeviceID))
	})
}

func LoadConfig(ctx context.Context, root, deviceID string) (ClientConfig, error) {
	if err := ctx.Err(); err != nil {
		return ClientConfig{}, err
	}
	var encoded []byte
	err := withDeviceLock(ctx, root, deviceID, true, func() error {
		file, err := os.Open(configPath(root, deviceID))
		if err != nil {
			return err
		}
		defer file.Close()
		encoded, err = io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
		if err != nil {
			return err
		}
		if len(encoded) > maxConfigBytes {
			return errors.New("team config exceeds size limit")
		}
		return nil
	})
	if err != nil {
		return ClientConfig{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var config ClientConfig
	if err := decoder.Decode(&config); err != nil {
		return ClientConfig{}, fmt.Errorf("decode team config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ClientConfig{}, errors.New("team config has trailing content")
	}
	if config.DeviceID != deviceID {
		return ClientConfig{}, errors.New("team config device id does not match path")
	}
	return config, nil
}

func withDeviceLock(ctx context.Context, root, deviceID string, readOnly bool, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Dir(configPath(root, deviceID))
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	lock := flock.New(deviceLockPath(root, deviceID))
	var locked bool
	var err error
	if readOnly {
		locked, err = lock.TryRLockContext(ctx, 50*time.Millisecond)
	} else {
		locked, err = lock.TryLockContext(ctx, 50*time.Millisecond)
	}
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("team device is busy")
	}
	defer lock.Unlock()
	return action()
}

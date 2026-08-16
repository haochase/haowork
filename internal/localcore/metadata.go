package localcore

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidMetadata = errors.New("invalid local core metadata")

type Metadata struct {
	ProjectID  string    `json:"project_id"`
	Endpoint   string    `json:"endpoint"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	ControlKey string    `json:"control_key"`
}

func ReadMetadata(projectRoot string) (Metadata, error) {
	data, err := os.ReadFile(metadataPath(projectRoot))
	if err != nil {
		return Metadata{}, err
	}

	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: decode core.json: %v", ErrInvalidMetadata, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Metadata{}, fmt.Errorf("%w: core.json contains multiple values", ErrInvalidMetadata)
		}
		return Metadata{}, fmt.Errorf("%w: trailing core.json content: %v", ErrInvalidMetadata, err)
	}
	if err := validateMetadata(metadata); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func writeMetadata(projectRoot string, metadata Metadata) error {
	if err := validateMetadata(metadata); err != nil {
		return err
	}
	directory := runtimeDir(projectRoot)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}

	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".core-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, metadataPath(projectRoot))
}

func removeMetadataIfOwned(projectRoot string, owner Metadata) error {
	current, err := ReadMetadata(projectRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.ControlKey != owner.ControlKey {
		return nil
	}
	if err := os.Remove(metadataPath(projectRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeStaleMetadata(projectRoot string) error {
	if err := os.Remove(metadataPath(projectRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func runtimeDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".haowork", "runtime")
}

func metadataPath(projectRoot string) string {
	return filepath.Join(runtimeDir(projectRoot), "core.json")
}

func newControlKey() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validateMetadata(metadata Metadata) error {
	if strings.TrimSpace(metadata.ProjectID) == "" {
		return fmt.Errorf("%w: project id is required", ErrInvalidMetadata)
	}
	if err := validateEndpoint(metadata.Endpoint); err != nil {
		return err
	}
	if metadata.PID <= 0 {
		return fmt.Errorf("%w: pid must be positive", ErrInvalidMetadata)
	}
	if metadata.StartedAt.IsZero() {
		return fmt.Errorf("%w: started at is required", ErrInvalidMetadata)
	}
	controlKey, err := base64.RawURLEncoding.DecodeString(metadata.ControlKey)
	if err != nil || len(controlKey) != 32 {
		return fmt.Errorf("%w: control key must be a 32-byte base64url value", ErrInvalidMetadata)
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%w: invalid endpoint: %v", ErrInvalidMetadata, err)
	}
	if err := validateLoopbackHTTPURL(parsed); err != nil {
		return err
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: endpoint must not include a path, query, or fragment", ErrInvalidMetadata)
	}
	return nil
}

func validateLoopbackHTTPURL(parsed *url.URL) error {
	if parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return fmt.Errorf("%w: endpoint must be an HTTP loopback address", ErrInvalidMetadata)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: endpoint port is invalid", ErrInvalidMetadata)
	}
	if host := net.ParseIP(parsed.Hostname()); host == nil || !host.IsLoopback() {
		return fmt.Errorf("%w: endpoint host must be loopback", ErrInvalidMetadata)
	}
	return nil
}

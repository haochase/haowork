package githubscm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const (
	maxConfigBytes   = 64 * 1024
	maxCursorBytes   = 1024 * 1024
	maxMonitoredRefs = 32
	maxETags         = 2048
)

type Config struct {
	LocalRepositoryID   string    `json:"local_repository_id"`
	RemoteID            string    `json:"remote_id"`
	Owner               string    `json:"owner"`
	Repository          string    `json:"repository"`
	GitHubRepositoryID  int64     `json:"github_repository_id"`
	MonitoredRefs       []string  `json:"monitored_refs"`
	InitialLookbackDays int       `json:"initial_lookback_days"`
	RegisteredAt        time.Time `json:"registered_at"`
}

type Cursor struct {
	GitHubRepositoryID int64             `json:"github_repository_id"`
	LastSuccessfulSync time.Time         `json:"last_successful_sync,omitempty"`
	OverlapSince       time.Time         `json:"overlap_since,omitempty"`
	ETags              map[string]string `json:"etags,omitempty"`
	RefOIDs            map[string]string `json:"ref_oids,omitempty"`
	ActivePullHeads    map[int]string    `json:"active_pull_heads,omitempty"`
	RateLimitReset     time.Time         `json:"rate_limit_reset,omitempty"`
}

type TokenSource interface {
	Token(context.Context) (string, error)
}

type EnvironmentTokenSource struct{}

func (EnvironmentTokenSource) Token(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return strings.TrimSpace(os.Getenv("HAOWORK_GITHUB_TOKEN")), nil
}

type FileStore struct {
	root string
}

func NewFileStore(root string) *FileStore {
	return &FileStore{root: filepath.Clean(root)}
}

func (store *FileStore) SaveConfig(ctx context.Context, config Config) error {
	config.MonitoredRefs = canonicalStrings(config.MonitoredRefs)
	if err := validateConfig(config); err != nil {
		return err
	}
	return store.writeJSON(ctx, store.configPath(), maxConfigBytes, config)
}

func (store *FileStore) LoadConfig(ctx context.Context) (Config, error) {
	var config Config
	if err := store.readJSON(ctx, store.configPath(), maxConfigBytes, &config); err != nil {
		return Config{}, err
	}
	if err := validateConfig(config); err != nil {
		return Config{}, fmt.Errorf("validate GitHub config: %w", err)
	}
	if !equalStrings(config.MonitoredRefs, canonicalStrings(config.MonitoredRefs)) {
		return Config{}, errors.New("GitHub config refs are not canonical")
	}
	return config, nil
}

func (store *FileStore) SaveCursor(ctx context.Context, cursor Cursor) error {
	if err := validateCursor(cursor); err != nil {
		return err
	}
	return store.writeJSON(ctx, store.cursorPath(), maxCursorBytes, cursor)
}

func (store *FileStore) LoadCursor(ctx context.Context) (Cursor, error) {
	var cursor Cursor
	if err := store.readJSON(ctx, store.cursorPath(), maxCursorBytes, &cursor); err != nil {
		return Cursor{}, err
	}
	if err := validateCursor(cursor); err != nil {
		return Cursor{}, fmt.Errorf("validate GitHub cursor: %w", err)
	}
	return cursor, nil
}

func (store *FileStore) configPath() string {
	return filepath.Join(store.root, ".haowork", "runtime", "scm", "github.json")
}

func (store *FileStore) cursorPath() string {
	return filepath.Join(store.root, ".haowork", "runtime", "scm", "github.cursor.json")
}

func (store *FileStore) lockPath() string {
	return filepath.Join(store.root, ".haowork", "runtime", "scm", "github.lock")
}

func (store *FileStore) writeJSON(ctx context.Context, target string, limit int, value any) error {
	if store == nil || strings.TrimSpace(store.root) == "" {
		return errors.New("GitHub runtime store root is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > limit {
		return errors.New("GitHub runtime state exceeds size limit")
	}
	return store.withLock(ctx, false, func() error {
		directory := filepath.Dir(target)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(directory, "github-*.json")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if err := temporary.Chmod(0o600); err != nil {
			_ = temporary.Close()
			return err
		}
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
		return os.Rename(temporaryPath, target)
	})
}

func (store *FileStore) readJSON(ctx context.Context, target string, limit int, destination any) error {
	if store == nil || strings.TrimSpace(store.root) == "" {
		return errors.New("GitHub runtime store root is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.withLock(ctx, true, func() error {
		file, err := os.Open(target)
		if err != nil {
			return err
		}
		defer file.Close()
		encoded, err := io.ReadAll(io.LimitReader(file, int64(limit+1)))
		if err != nil {
			return err
		}
		if len(encoded) > limit {
			return errors.New("GitHub runtime state exceeds size limit")
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(destination); err != nil {
			return fmt.Errorf("decode GitHub runtime state: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return errors.New("GitHub runtime state has trailing content")
		}
		return nil
	})
}

func (store *FileStore) withLock(ctx context.Context, readOnly bool, action func() error) error {
	directory := filepath.Dir(store.lockPath())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	lock := flock.New(store.lockPath())
	var locked bool
	var err error
	if readOnly {
		locked, err = lock.TryRLockContext(ctx, 100*time.Millisecond)
	} else {
		locked, err = lock.TryLockContext(ctx, 100*time.Millisecond)
	}
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("GitHub runtime state is busy")
	}
	defer lock.Unlock()
	return action()
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.LocalRepositoryID) == "" || strings.TrimSpace(config.RemoteID) == "" || !validGitHubOwner(config.Owner) || !validGitHubRepository(config.Repository) || config.GitHubRepositoryID <= 0 || config.RegisteredAt.IsZero() {
		return errors.New("GitHub config identity is invalid")
	}
	if config.InitialLookbackDays < 1 || config.InitialLookbackDays > 365 {
		return errors.New("GitHub initial lookback must be between 1 and 365 days")
	}
	if len(config.MonitoredRefs) == 0 || len(config.MonitoredRefs) > maxMonitoredRefs {
		return errors.New("GitHub config must contain between 1 and 32 monitored refs")
	}
	for _, ref := range config.MonitoredRefs {
		if !validBranchRef(ref) {
			return fmt.Errorf("invalid monitored ref %q", ref)
		}
	}
	return nil
}

func validateCursor(cursor Cursor) error {
	if cursor.GitHubRepositoryID <= 0 {
		return errors.New("GitHub cursor repository ID is required")
	}
	if len(cursor.ETags) > maxETags {
		return errors.New("GitHub cursor contains too many ETags")
	}
	for key, value := range cursor.ETags {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return errors.New("GitHub cursor ETags cannot be blank")
		}
	}
	for ref, oid := range cursor.RefOIDs {
		if !validBranchRef(ref) || strings.TrimSpace(oid) == "" {
			return errors.New("GitHub cursor contains an invalid ref")
		}
	}
	for number, oid := range cursor.ActivePullHeads {
		if number <= 0 || strings.TrimSpace(oid) == "" {
			return errors.New("GitHub cursor contains an invalid pull head")
		}
	}
	return nil
}

func canonicalStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validBranchRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.Contains(value, "//") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("/._-", character) {
			continue
		}
		return false
	}
	return true
}

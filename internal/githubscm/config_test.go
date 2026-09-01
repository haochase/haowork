package githubscm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigAndCursorAreAtomicStrictAndCredentialFree(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	config := Config{
		LocalRepositoryID: "SCM-001", RemoteID: "RSCM-001", Owner: "haochase", Repository: "haowork",
		GitHubRepositoryID: 42, MonitoredRefs: []string{"refs/heads/release", "refs/heads/main"}, InitialLookbackDays: 90,
		RegisteredAt: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC),
	}
	if err := store.SaveConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".haowork", "runtime", "scm", "github.json")
	encoded, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	lowered := strings.ToLower(string(encoded))
	if strings.Contains(lowered, "token") || strings.Contains(lowered, "secret") {
		t.Fatalf("config leaked a credential field: %s", encoded)
	}
	loaded, err := store.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(loaded.MonitoredRefs, ",") != "refs/heads/main,refs/heads/release" {
		t.Fatalf("monitored refs = %#v", loaded.MonitoredRefs)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o, want private", info.Mode().Perm())
	}

	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
	cursor := Cursor{
		GitHubRepositoryID: 42, LastSuccessfulSync: now, OverlapSince: now.Add(-24 * time.Hour),
		ETags: map[string]string{"/repos/haochase/haowork": `"etag-1"`}, RefOIDs: map[string]string{"refs/heads/main": strings.Repeat("a", 40)},
		ActivePullHeads: map[int]string{1: strings.Repeat("b", 40)}, RateLimitReset: now.Add(time.Hour),
	}
	if err := store.SaveCursor(context.Background(), cursor); err != nil {
		t.Fatal(err)
	}
	loadedCursor, err := store.LoadCursor(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loadedCursor.GitHubRepositoryID != 42 || loadedCursor.ETags["/repos/haochase/haowork"] != `"etag-1"` {
		t.Fatalf("cursor = %#v", loadedCursor)
	}
}

func TestConfigRejectsUnknownTrailingAndOversizedState(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	path := filepath.Join(root, ".haowork", "runtime", "scm", "github.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"local_repository_id":"SCM-1","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(context.Background()); err == nil {
		t.Fatal("LoadConfig() accepted an unknown field")
	}
	if err := os.WriteFile(path, []byte(`{"local_repository_id":"SCM-1"}{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(context.Background()); err == nil {
		t.Fatal("LoadConfig() accepted trailing JSON")
	}
	config := Config{
		LocalRepositoryID: "SCM-1", RemoteID: "RSCM-1", Owner: "owner", Repository: "repo", GitHubRepositoryID: 1,
		MonitoredRefs: make([]string, maxMonitoredRefs+1), InitialLookbackDays: 90, RegisteredAt: time.Now().UTC(),
	}
	for index := range config.MonitoredRefs {
		config.MonitoredRefs[index] = "refs/heads/branch-" + strings.Repeat("x", index+1)
	}
	if err := store.SaveConfig(context.Background(), config); err == nil {
		t.Fatal("SaveConfig() accepted too many refs")
	}
	cursor := Cursor{GitHubRepositoryID: 1, ETags: make(map[string]string, maxETags+1)}
	for index := 0; index <= maxETags; index++ {
		cursor.ETags[strings.Repeat("x", index+1)] = `"etag"`
	}
	if err := store.SaveCursor(context.Background(), cursor); err == nil {
		t.Fatal("SaveCursor() accepted too many ETags")
	}
}

func TestEnvironmentTokenSourceReadsOnlyAtCallTime(t *testing.T) {
	const name = "HAOWORK_GITHUB_TOKEN"
	t.Setenv(name, "")
	source := EnvironmentTokenSource{}
	if token, err := source.Token(context.Background()); err != nil || token != "" {
		t.Fatalf("empty Token() = %q, %v", token, err)
	}
	t.Setenv(name, "github-token-value")
	if token, err := source.Token(context.Background()); err != nil || token != "github-token-value" {
		t.Fatalf("Token() = %q, %v", token, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Token(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Token() error = %v", err)
	}
}

func TestFileStoreConcurrentSaveLoadKeepsStrictState(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	config := Config{
		LocalRepositoryID: "SCM-1", RemoteID: "RSCM-1", Owner: "owner", Repository: "repo", GitHubRepositoryID: 1,
		MonitoredRefs: []string{"refs/heads/main"}, InitialLookbackDays: 90, RegisteredAt: time.Now().UTC(),
	}
	if err := store.SaveConfig(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 8; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := store.SaveConfig(context.Background(), config); err != nil {
				t.Errorf("SaveConfig() error = %v", err)
			}
			if loaded, err := store.LoadConfig(context.Background()); err != nil || loaded.GitHubRepositoryID != config.GitHubRepositoryID {
				t.Errorf("LoadConfig() = %#v, %v", loaded, err)
			}
		}()
	}
	group.Wait()
}

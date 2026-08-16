package teamsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigWriteIsAtomicAndRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	config := ClientConfig{
		Endpoint: "https://team.example.test",
		DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: "PRJ-1",
	}
	if err := SaveConfig(context.Background(), root, config); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	path := filepath.Join(root, ".haowork", "local", "DEV-1", "team.json")
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Contains(strings.ToLower(string(bytes)), "token") {
		t.Fatalf("team config must not persist a token: %s", bytes)
	}
	if err := os.WriteFile(path, []byte(`{"endpoint":"https://team.example.test","device_id":"DEV-1","unknown":true}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := LoadConfig(context.Background(), root, "DEV-1"); err == nil {
		t.Fatal("LoadConfig() accepted an unknown field")
	}
}

func TestConfigRejectsOversizedFileAndConcurrentSaveLoad(t *testing.T) {
	root := t.TempDir()
	config := ClientConfig{Endpoint: "https://team.example.test", DeviceID: "DEV-1", EnvironmentID: "dev", PrincipalID: "USR-1", TeamProjectID: "PRJ-1"}
	if err := SaveConfig(context.Background(), root, config); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".haowork", "local", "DEV-1", "team.json")
	config.Endpoint = "https://" + strings.Repeat("x", 128*1024) + ".example.test"
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(context.Background(), root, "DEV-1"); err == nil {
		t.Fatal("LoadConfig() accepted an oversized file")
	}
	config.Endpoint = "https://team.example.test"
	if err := SaveConfig(context.Background(), root, config); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := SaveConfig(context.Background(), root, config); err != nil {
				t.Errorf("SaveConfig() error = %v", err)
			}
			if _, err := LoadConfig(context.Background(), root, "DEV-1"); err != nil {
				t.Errorf("LoadConfig() error = %v", err)
			}
		}()
	}
	group.Wait()
}

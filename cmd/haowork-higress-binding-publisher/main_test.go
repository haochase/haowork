package main

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseCommandAllowsOnlyFixedStatePreparation(t *testing.T) {
	command, statePath, err := parseCommand([]string{"prepare-state-dir", managedStatePath})
	if err != nil || command != "prepare-state-dir" || statePath != managedStatePath {
		t.Fatalf("command = %q, state path = %q, err = %v", command, statePath, err)
	}

	invalid := [][]string{
		{"prepare-state-dir"},
		{"prepare-state-dir", managedStatePath, "extra"},
		{"prepare-state-dir", "publisher-state/managed-consumers.json"},
		{"prepare-state-dir", "/var/lib/haowork/publisher-state/../managed-consumers.json"},
		{"prepare-state-dir", "/tmp/managed-consumers.json"},
		{"prepare-state-dir", managedStatePath + "\x00"},
		{"unknown", managedStatePath},
	}
	for _, args := range invalid {
		if _, _, err := parseCommand(args); err == nil {
			t.Fatalf("accepted command arguments %#v", args)
		}
	}
}

func TestPrepareStateDirectoryRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse behavior is outside the Linux container contract")
	}
	root := t.TempDir()
	target := t.TempDir()
	link := root + "/publisher-state"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareStateDirectory(link+"/managed-consumers.json", publisherUID, publisherGID); err == nil {
		t.Fatal("accepted symlinked state directory")
	}
}

func TestLoadConfigRequiresFixedMCPServerAndAllCredentials(t *testing.T) {
	values := map[string]string{
		envConsoleURL: "http://127.0.0.1:8080", envConsoleUser: "admin", envConsolePass: "secret",
		envMCPServer: "haowork-mcp", envBindingsPath: "/var/lib/haowork/bindings/mcp-bindings.json",
		envStatePath: "/var/lib/haowork/publisher-state/managed-consumers.json", envPollInterval: "5s",
		envRetryInterval: "250ms", envMaxAttempts: "3",
		envAllowedHosts: "higress-console.haowork-public.svc.cluster.local:8080", envAllowLoopback: "false",
	}
	configuration, err := loadConfig(func(key string) string { return values[key] })
	if err != nil || configuration.pollInterval != 5*time.Second || configuration.retryInterval != 250*time.Millisecond || configuration.maxAttempts != 3 {
		t.Fatalf("config = %#v, err = %v", configuration, err)
	}
	values[envAllowedHosts] = ""
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted missing Console host allowlist")
	}
	values[envAllowedHosts] = "higress-console.haowork-public.svc.cluster.local:8080"
	values[envMCPServer] = "other-mcp"
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted non-owned MCP server")
	}
	values[envMCPServer] = "haowork-mcp"
	values[envConsolePass] = ""
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted missing Console password")
	}
	values[envConsolePass] = "secret"
	values[envStatePath] = ""
	if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("accepted missing managed state path")
	}
}

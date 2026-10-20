package teamai_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/haochase/haowork/internal/teamai"
)

const publicSkillPath = "skills/haowork-governed-coding/SKILL.md"
const publicSkill = "---\nname: haowork-governed-coding\ndescription: Governance contract\n---\n"

func publicConfig() teamai.Config {
	sum := sha256.Sum256([]byte(publicSkill))
	return teamai.Config{
		SchemaVersion: "1", TeamRepo: "https://example.invalid/team/assets",
		TeamRevision: strings.Repeat("a", 40), Scope: "project", AutoUpdate: "manual",
		RequiredAssets: []teamai.AssetSpec{{Path: publicSkillPath, Kind: "skill", SHA256: hex.EncodeToString(sum[:])}},
	}
}

func TestPublicTeamAIConfigAndAssetIntegrity(t *testing.T) {
	for _, scenario := range []struct {
		name, content string
		missing       bool
		ready         bool
	}{
		{name: "valid bytes", content: publicSkill, ready: true},
		{name: "missing", missing: true},
		{name: "changed bytes", content: publicSkill + "changed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			if !scenario.missing {
				filename := filepath.Join(root, filepath.FromSlash(publicSkillPath))
				if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(scenario.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := teamai.Verify(context.Background(), root, publicConfig(), root)
			if result.Ready != scenario.ready || (err == nil) != scenario.ready {
				t.Fatalf("ready=%t err=%v", result.Ready, err)
			}
		})
	}
	for _, change := range []func(*teamai.Config){
		func(cfg *teamai.Config) { cfg.TeamRevision = "main" },
		func(cfg *teamai.Config) { cfg.Scope = "global" },
		func(cfg *teamai.Config) { cfg.AutoUpdate = "automatic" },
		func(cfg *teamai.Config) { cfg.RequiredAssets[0].Path = "../outside" },
	} {
		cfg := publicConfig()
		change(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatal("unsafe configuration was accepted")
		}
	}
}

type publicRunner struct {
	initialized bool
	calls       int
}

func (runner *publicRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	runner.calls++
	if name != "teamai" {
		panic("unexpected executable")
	}
	if len(args) == 1 && args[0] == "--version" {
		return []byte("0.27.0"), nil
	}
	if len(args) != 2 || args[0] != "doctor" || args[1] != "--json" {
		panic("unexpected mutation or command")
	}
	if runner.initialized {
		return []byte(`{"ok":true,"scope":"project","checks":[{"name":"Team config","ok":true}]}`), nil
	}
	return []byte(`{"ok":false,"scope":null,"checks":[]}`), nil
}

func TestPublicTeamAIDoctorRequiresActualInitialization(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		runner := &publicRunner{initialized: initialized}
		detector := teamai.Detector{Runner: runner, FS: fstest.MapFS{publicSkillPath: {Data: []byte(publicSkill)}}}
		result, err := detector.Doctor(context.Background(), ".", publicConfig())
		if result.Initialized != initialized || (err == nil) != initialized || runner.calls != 2 {
			t.Fatalf("initialized=%t calls=%d err=%v", result.Initialized, runner.calls, err)
		}
	}
}

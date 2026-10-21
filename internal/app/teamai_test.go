package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/teamai"
	"go.yaml.in/yaml/v3"
)

const teamAISkill = "skills/haowork-governed-coding/SKILL.md"
const teamAIContent = "---\nname: haowork-governed-coding\ndescription: Governance\n---\n# Governed\n"

func teamAIFixture(t *testing.T) (string, teamai.Config) {
	t.Helper()
	root := t.TempDir()
	sum := sha256.Sum256([]byte(teamAIContent))
	cfg := teamai.Config{SchemaVersion: "1", TeamRepo: "https://github.com/example/team", TeamRevision: strings.Repeat("a", 40), Scope: "project", AutoUpdate: "manual", RequiredAssets: []teamai.AssetSpec{{Path: teamAISkill, Kind: "skill", SHA256: hex.EncodeToString(sum[:])}}}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".haowork"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".haowork", "teamai.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, cfg
}

type teamAIRunner struct {
	initialized bool
	calls       int
}

func (r *teamAIRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls++
	if len(args) == 1 && args[0] == "--version" {
		return []byte("0.27.0\n"), nil
	}
	if r.initialized {
		return []byte(`{"ok":true,"scope":"project","checks":[{"name":"Team config","ok":true}]}`), nil
	}
	return []byte(`{"ok":false,"scope":null,"checks":[{"name":"TeamAI is not initialized","ok":false,"detail":"private absolute path"}]}`), errors.New("private stderr")
}

func TestTeamAIDoctorMissingConfigurationIsReadOnly(t *testing.T) {
	root := t.TempDir()
	runner := &teamAIRunner{}
	result, err := DoctorTeamAI(context.Background(), root, "", teamai.Detector{Runner: runner})
	if err != nil || result.Configured || result.Status != "Unbound" || runner.calls != 0 {
		t.Fatalf("doctor = %+v, %v", result, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("doctor wrote files: %v, %v", entries, err)
	}
}

func TestTeamAIDoctorRequiresOfficialInitialization(t *testing.T) {
	root, _ := teamAIFixture(t)
	for _, initialized := range []bool{false, true} {
		runner := &teamAIRunner{initialized: initialized}
		result, err := DoctorTeamAI(context.Background(), root, "", teamai.Detector{Runner: runner})
		if result.Doctor.Initialized != initialized || !result.Doctor.Installed || err == nil {
			t.Fatalf("doctor = %+v, %v", result, err)
		}
		encoded, _ := json.Marshal(result)
		if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), "private") {
			t.Fatalf("doctor leaked data: %s", encoded)
		}
	}
}

func TestTeamAIBindAuthorityConsistencyAndIdempotency(t *testing.T) {
	ctx := context.Background()
	root, cfg := teamAIFixture(t)
	service, repository := newWorkflowService(t)
	before := len(repository.events)
	for _, actor := range []model.Actor{agent("AGT-1"), {ID: "USR-1", Kind: model.ActorHuman, Role: model.RoleContributor}} {
		if _, err := service.BindTeamAI(ctx, root, BindTeamAIInput{TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Actor: actor}); !errors.Is(err, ErrApprovalRequired) {
			t.Fatalf("authority error = %v", err)
		}
	}
	if _, err := service.BindTeamAI(ctx, root, BindTeamAIInput{TeamRepo: cfg.TeamRepo, TeamRevision: strings.Repeat("b", 40), Actor: owner("USR-1")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatch error = %v", err)
	}
	if len(repository.events) != before {
		t.Fatal("rejected binding wrote events")
	}
	input := BindTeamAIInput{TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Actor: owner("USR-1")}
	binding, err := service.BindTeamAI(ctx, root, input)
	if err != nil || binding.Status != "Bound" {
		t.Fatalf("bind = %+v, %v", binding, err)
	}
	again, err := service.BindTeamAI(ctx, root, input)
	if err != nil || !reflect.DeepEqual(binding, again) || len(repository.events) != before+1 {
		t.Fatalf("repeat bind = %+v, %v", again, err)
	}
}

func TestTeamAIVerifyRealAssetsAndIdempotency(t *testing.T) {
	ctx := context.Background()
	root, cfg := teamAIFixture(t)
	service, repository := newWorkflowService(t)
	beforeState, _ := service.Status(ctx)
	detector := teamai.Detector{Runner: &teamAIRunner{initialized: true}}
	if _, err := service.VerifyTeamAI(ctx, root, "", detector); err == nil {
		t.Fatal("verified without binding")
	}
	binding, err := service.BindTeamAI(ctx, root, BindTeamAIInput{TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Actor: owner("USR-1")})
	if err != nil {
		t.Fatal(err)
	}
	before := len(repository.events)
	if _, err := service.VerifyTeamAI(ctx, root, "", detector); err == nil || len(repository.events) != before {
		t.Fatal("missing assets recorded verification")
	}
	path := filepath.Join(root, filepath.FromSlash(teamAISkill))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(teamAIContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyTeamAI(ctx, root, "", teamai.Detector{Runner: &teamAIRunner{}}); err == nil || len(repository.events) != before {
		t.Fatal("uninitialized CLI recorded verification")
	}
	result, err := service.VerifyTeamAI(ctx, root, "", detector)
	if err != nil || result.Binding.Status != "Ready" || len(result.Binding.AssetDigest) != 64 {
		t.Fatalf("verify = %+v, %v", result, err)
	}
	canonical, _ := json.Marshal([]struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		SHA256 string `json:"sha256"`
	}{{teamAISkill, "skill", cfg.RequiredAssets[0].SHA256}})
	sum := sha256.Sum256(canonical)
	if result.Binding.AssetDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s", result.Binding.AssetDigest)
	}
	if _, err := service.VerifyTeamAI(ctx, root, "", detector); err != nil || len(repository.events) != before+1 {
		t.Fatalf("repeat verify wrote events: %v", err)
	}
	afterState, _ := service.Status(ctx)
	delete(afterState.TeamAIBindings, binding.ID)
	if !reflect.DeepEqual(beforeState, afterState) {
		t.Fatal("TeamAI changed governed workflow state")
	}
}

func TestTeamAIVerifyExternalAssetRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows CLI shim fixture")
	}
	ctx := context.Background()
	root, cfg := teamAIFixture(t)
	assets := t.TempDir()
	name := filepath.Join(assets, filepath.FromSlash(teamAISkill))
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(teamAIContent), 0o600); err != nil {
		t.Fatal(err)
	}
	service, _ := newWorkflowService(t)
	if _, err := service.BindTeamAI(ctx, root, BindTeamAIInput{TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Actor: owner("USR-1")}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "@echo off\r\nif \"%~1\"==\"--version\" (echo 0.27.0 & exit /b 0)\r\nif exist project-marker (\r\necho {\"ok\":true,\"scope\":\"project\",\"checks\":[{\"name\":\"Team config\",\"ok\":true}]}\r\n) else (\r\necho {\"ok\":false,\"scope\":null,\"checks\":[{\"name\":\"TeamAI is not initialized\",\"ok\":false}]}\r\nexit /b 1\r\n)\r\n"
	if err := os.WriteFile(filepath.Join(bin, "teamai.cmd"), []byte(shim), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project-marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := service.VerifyTeamAI(ctx, root, assets, teamai.Detector{})
	if err != nil || result.Binding.Status != "Ready" {
		t.Fatalf("external asset verify=%+v, %v", result, err)
	}
}

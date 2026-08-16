package capsule_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/capsule"
)

func TestInitCreatesReadablePortableLayout(t *testing.T) {
	root := t.TempDir()
	created := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)

	manifest, err := capsule.Init(root, capsule.InitInput{
		ProjectID: "PRJ-TEST",
		Name:      "demo",
		ActorID:   "USR-ALICE",
		CreatedAt: created,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProtocolVersion != capsule.ProtocolVersion {
		t.Fatalf("protocol = %q", manifest.ProtocolVersion)
	}
	for _, path := range []string{
		"manifest.yaml", "events.jsonl", "design", "summary", "records",
		"identities", "evidence", "transfers", "runtime",
	} {
		if _, err := os.Stat(filepath.Join(root, ".haowork", path)); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}

	loaded, err := capsule.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != manifest {
		t.Fatalf("Load() = %#v, want %#v", loaded, manifest)
	}
}

func TestFindWalksToProjectRoot(t *testing.T) {
	root := t.TempDir()
	_, err := capsule.Init(root, capsule.InitInput{
		ProjectID: "PRJ-TEST", Name: "demo", ActorID: "USR-ALICE", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "src", "service")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := capsule.Find(nested)
	if err != nil {
		t.Fatal(err)
	}
	if found != root {
		t.Fatalf("Find() = %q, want %q", found, root)
	}
}

func TestInitRefusesExistingCapsule(t *testing.T) {
	root := t.TempDir()
	input := capsule.InitInput{ProjectID: "PRJ-TEST", Name: "demo", ActorID: "USR-ALICE", CreatedAt: time.Now().UTC()}
	if _, err := capsule.Init(root, input); err != nil {
		t.Fatal(err)
	}
	if _, err := capsule.Init(root, input); err == nil {
		t.Fatal("second Init() succeeded, want refusal")
	}
}

func TestInitRejectsZeroCreatedAtWithoutCreatingCapsule(t *testing.T) {
	root := t.TempDir()

	if _, err := capsule.Init(root, capsule.InitInput{
		ProjectID: "PRJ-TEST",
		Name:      "demo",
		ActorID:   "USR-ALICE",
	}); err == nil {
		t.Fatal("Init() succeeded, want created-at validation error")
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork")); !os.IsNotExist(err) {
		t.Fatalf("capsule exists after invalid Init(): %v", err)
	}
}

func TestLoadRejectsProtocolErrors(t *testing.T) {
	valid := "protocol_version: 0.1.0\nproject_id: PRJ-TEST\nname: demo\ncurrent_goal_version: 1\ncreated_at: 2026-08-05T01:02:03Z\ncreated_by: USR-ALICE\n"

	for _, test := range []struct {
		name string
		data string
	}{
		{name: "unknown field", data: valid + "unexpected: value\n"},
		{name: "trailing document", data: valid + "---\nprotocol_version: 0.1.0\n"},
		{name: "missing required field", data: "protocol_version: 0.1.0\nproject_id: PRJ-TEST\nname: demo\ncurrent_goal_version: 1\ncreated_at: 2026-08-05T01:02:03Z\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			capsuleDir := filepath.Join(root, ".haowork")
			if err := os.Mkdir(capsuleDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(capsuleDir, "manifest.yaml"), []byte(test.data), 0o644); err != nil {
				t.Fatal(err)
			}

			if _, err := capsule.Load(root); err == nil {
				t.Fatal("Load() succeeded, want protocol error")
			}
		})
	}
}

func TestUpdateGoalVersionOnlyAdvancesOneVersionAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, err := capsule.Init(root, capsule.InitInput{
		ProjectID: "PRJ-TEST", Name: "demo", ActorID: "USR-ALICE", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := capsule.UpdateGoalVersion(root, 1, 3); err == nil {
		t.Fatal("UpdateGoalVersion() succeeded for a skipped version")
	}
	before, err := capsule.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if before.CurrentGoalVersion != 1 {
		t.Fatalf("manifest goal version = %d after rejected update, want 1", before.CurrentGoalVersion)
	}

	if err := capsule.UpdateGoalVersion(root, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := capsule.UpdateGoalVersion(root, 1, 2); err != nil {
		t.Fatalf("UpdateGoalVersion() idempotent retry error = %v", err)
	}
	after, err := capsule.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentGoalVersion != 2 {
		t.Fatalf("manifest goal version = %d, want 2", after.CurrentGoalVersion)
	}
}

func TestUpdateGoalVersionUsesKnownFieldsBeforeReplacingManifest(t *testing.T) {
	root := t.TempDir()
	if _, err := capsule.Init(root, capsule.InitInput{
		ProjectID: "PRJ-TEST", Name: "demo", ActorID: "USR-ALICE", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".haowork", "manifest.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("unknown_field: refuse-write\n")...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := capsule.UpdateGoalVersion(root, 1, 2); err == nil {
		t.Fatal("UpdateGoalVersion() succeeded with an unknown manifest field")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatal("UpdateGoalVersion() rewrote a manifest that failed known-fields validation")
	}
}

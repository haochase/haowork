package skillruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryLoadsExactlyElevenCanonicalSkills(t *testing.T) {
	registry, err := Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(registry.Definitions()); got != 11 {
		t.Fatalf("definition count = %d, want 11", got)
	}
	definition, err := registry.Resolve("plan", "")
	if err != nil {
		t.Fatalf("Resolve(plan, current) error = %v", err)
	}
	if definition.Version != "1.0.0" {
		t.Fatalf("resolved version = %q, want 1.0.0", definition.Version)
	}
	if _, err := registry.Resolve("plan", "1.0.1"); err == nil {
		t.Fatal("Resolve accepted an undeclared exact version")
	}
}

func TestRegistryRejectsUnknownFieldsDuplicateVersionsAndInvalidSchema(t *testing.T) {
	root := copyCanonicalSkills(t)
	planPath := filepath.Join(root, "core", "plan.yaml")
	plan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	writeDefinition(t, root, "core/plan.yaml", string(plan)+"unknown: rejected\n")
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted an unknown YAML field")
	}

	root = copyCanonicalSkills(t)
	writeDefinition(t, root, "core/plan-v2.yaml", validDefinitionYAML("plan"))
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted a duplicate skill version")
	}

	root = copyCanonicalSkills(t)
	writeDefinition(t, root, "core/plan.yaml", "name: plan\nversion: 1.0.0\ninput_schema: '[]'\noutput_schema: '{\"type\":\"object\"}'\nrisk: L0\nallowed_functions: [manager]\nrequired_context: false\nrequired_lease: false\nsupported_environments: [public]\nadapter: core.plan\ntimeout: 30s\nretry_policy: workflow-owned\nevidence_policy: none\n")
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted a non-object JSON schema")
	}

	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte(validDefinitionYAML("outside")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(root, outside); err == nil {
		t.Fatal("LoadFile accepted a definition outside its registry root")
	}
}

func TestRegistryRequiresExactlyCanonicalSkillSet(t *testing.T) {
	root := copyCanonicalSkills(t)
	writeDefinition(t, root, "core/unapproved.yaml", validDefinitionYAML("unapproved"))
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted an additional non-canonical skill")
	}
	if err := os.Remove(filepath.Join(root, "core", "unapproved.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "core", "plan.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted a registry missing a canonical skill")
	}
}

func TestRegistryRejectsInvalidNestedJSONSchema(t *testing.T) {
	root := copyCanonicalSkills(t)
	invalid := strings.Replace(validDefinitionYAML("plan"), `"additionalProperties":false}`, `"properties":{"x":3}}`, 1)
	writeDefinition(t, root, "core/plan.yaml", invalid)
	if _, err := Load(root); err == nil {
		t.Fatal("Load accepted an invalid nested property schema")
	}
}

func writeDefinition(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func validDefinitionYAML(name string) string {
	return "name: " + name + "\nversion: 1.0.0\ninput_schema: '{\"type\":\"object\",\"additionalProperties\":false}'\noutput_schema: '{\"type\":\"object\",\"additionalProperties\":false}'\nrisk: L0\nallowed_functions: [manager]\nrequired_context: false\nrequired_lease: false\nsupported_environments: [public]\nadapter: core." + name + "\ntimeout: 30s\nretry_policy: workflow-owned\nevidence_policy: none\n"
}

func copyCanonicalSkills(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"core/plan.yaml", "core/context.yaml", "core/record.yaml", "core/history.yaml", "core/verify.yaml", "core/export.yaml", "core/import.yaml", "packs/cross-zone/advisory.yaml", "packs/cross-zone/mirror.yaml", "packs/cross-zone/patch.yaml", "packs/cross-zone/audit.yaml"} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		writeDefinition(t, root, name, string(data))
	}
	return root
}

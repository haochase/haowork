package skillruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryLoadsExactlyTwelveCanonicalSkills(t *testing.T) {
	registry, err := Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := len(registry.Definitions()); got != 12 {
		t.Fatalf("definition count = %d, want 12", got)
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
	if definition, err := registry.Resolve("submit_execution_report", "1.0.0"); err != nil || definition.Name != "submit_execution_report" {
		t.Fatalf("Resolve(submit_execution_report) = %#v, %v", definition, err)
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

func TestValidateJSONInputEnforcesReceiptSchemaBounds(t *testing.T) {
	registry, err := Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("submit_execution_report", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"assignment_id":"ASN-46fc8933c009dd44765c6f4b01aeaf9d","status":"succeeded","summary_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[],"checks":[]}`
	if err := ValidateJSONInput(definition, []byte(valid)); err != nil {
		t.Fatalf("valid receipt schema input rejected: %v", err)
	}
	for name, input := range map[string]string{
		"pattern":   strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"minLength": strings.Replace(valid, `"artifacts":[]`, `"artifacts":[{"kind":"","uri":"s3://bucket/key","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment_id":"public","size":0}]`, 1),
		"minimum":   strings.Replace(valid, `"artifacts":[]`, `"artifacts":[{"kind":"report","uri":"s3://bucket/key","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment_id":"public","size":-1}]`, 1),
		"maxItems":  strings.Replace(valid, `"checks":[]`, `"checks":[`+strings.Repeat(`{"name":"unit","status":"passed","evidence_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},`, 32)+`{"name":"unit","status":"passed","evidence_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateJSONInput(definition, []byte(input)); err == nil {
				t.Fatal("ValidateJSONInput accepted a value outside the schema bound")
			}
		})
	}
}

func TestReceiptSchemaRequiresArtifactEnvironmentIdentity(t *testing.T) {
	registry, err := Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("submit_execution_report", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"assignment_id":"ASN-46fc8933c009dd44765c6f4b01aeaf9d","status":"succeeded","summary_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[{"kind":"mission","uri":"s3://bucket/environments/public/missions/MSN-P005/artifacts/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","environment_id":"public","size":42}],"checks":[]}`
	if err := ValidateJSONInput(definition, []byte(valid)); err != nil {
		t.Fatalf("receipt artifact with environment identity rejected: %v", err)
	}
	missing := strings.Replace(valid, `,"environment_id":"public"`, "", 1)
	if err := ValidateJSONInput(definition, []byte(missing)); err == nil {
		t.Fatal("receipt artifact without environment identity accepted")
	}
}

func TestReceiptSchemaAcceptsOnlyCanonicalTransportAssignmentIDs(t *testing.T) {
	registry, err := Load(filepath.Join("..", "..", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("submit_execution_report", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"assignment_id":"ASN-46fc8933c009dd44765c6f4b01aeaf9d","status":"succeeded","summary_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifacts":[],"checks":[]}`
	if err := ValidateJSONInput(definition, []byte(valid)); err != nil {
		t.Fatalf("canonical transport Assignment ID rejected: %v", err)
	}
	for _, invalid := range []string{
		"ASN-46FC8933C009DD44765C6F4B01AEAF9D",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9dd",
		"ASG-46fc8933c009dd44765c6f4b01aeaf9d",
		"ASN-46fc8933c009dd44765c6f4b01aeaf9g",
	} {
		input := strings.Replace(valid, "ASN-46fc8933c009dd44765c6f4b01aeaf9d", invalid, 1)
		if err := ValidateJSONInput(definition, []byte(input)); err == nil {
			t.Fatalf("receipt schema accepted non-canonical Assignment ID %q", invalid)
		}
	}
}

func TestValidateJSONInputEnforcesDecimalNumberBounds(t *testing.T) {
	root := copyCanonicalSkills(t)
	document := strings.Replace(
		validDefinitionYAML("plan"),
		`input_schema: '{"type":"object","additionalProperties":false}'`,
		`input_schema: '{"type":"object","additionalProperties":false,"properties":{"score":{"type":"number","minimum":0.5,"maximum":1.5}},"required":["score"]}'`,
		1,
	)
	writeDefinition(t, root, "core/plan.yaml", document)
	registry, err := Load(root)
	if err != nil {
		t.Fatalf("Load decimal number schema: %v", err)
	}
	definition, err := registry.Resolve("plan", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"score":0.5}`, `{"score":1.25}`, `{"score":1.5}`} {
		if err := ValidateJSONInput(definition, []byte(input)); err != nil {
			t.Fatalf("bounded number %s rejected: %v", input, err)
		}
	}
	for _, input := range []string{`{"score":0.49}`, `{"score":1.51}`} {
		if err := ValidateJSONInput(definition, []byte(input)); err == nil {
			t.Fatalf("out-of-bounds number %s accepted", input)
		}
	}
}

func TestValidateJSONInputComparesLargeIntegerBoundsExactly(t *testing.T) {
	definition := Definition{InputSchema: []byte(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer","minimum":9007199254740993,"maximum":9007199254740993}},"required":["value"]}`)}
	if err := ValidateJSONInput(definition, []byte(`{"value":9007199254740993}`)); err != nil {
		t.Fatalf("exact large integer rejected: %v", err)
	}
	if err := ValidateJSONInput(definition, []byte(`{"value":9007199254740992}`)); err == nil {
		t.Fatal("large integer below exact minimum was accepted")
	}
	if err := ValidateJSONInput(definition, []byte(`{"value":9007199254740994}`)); err == nil {
		t.Fatal("large integer above exact maximum was accepted")
	}
	if err := ValidateJSONInput(definition, []byte(`{"value":9223372036854775808}`)); err == nil {
		t.Fatal("integer input exceeding int64 was accepted")
	}
	maximum := Definition{InputSchema: []byte(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"integer","maximum":9223372036854775807}},"required":["value"]}`)}
	if err := ValidateJSONInput(maximum, []byte(`{"value":9223372036854775807}`)); err != nil {
		t.Fatalf("int64 maximum rejected: %v", err)
	}
}

func TestRegistryRejectsIntegerBoundaryOutsideSupportedRange(t *testing.T) {
	schema := `{"type":"object","properties":{"value":{"type":"integer","minimum":9223372036854775808}}}`
	if _, err := validateJSONObjectSchema(schema); err == nil {
		t.Fatal("registry accepted an integer boundary exceeding int64")
	}
}

func TestValidateJSONInputSupportsFiniteExponentBounds(t *testing.T) {
	definition := Definition{InputSchema: []byte(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"number","minimum":1e3,"maximum":2e3}},"required":["value"]}`)}
	if err := ValidateJSONInput(definition, []byte(`{"value":1.5e3}`)); err != nil {
		t.Fatalf("finite exponent value rejected: %v", err)
	}
	if err := ValidateJSONInput(definition, []byte(`{"value":9.99e2}`)); err == nil {
		t.Fatal("exponent value below minimum was accepted")
	}
}

func TestValidateJSONInputRejectsNonFiniteAndOverflowNumbers(t *testing.T) {
	definition := Definition{InputSchema: []byte(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"number"}},"required":["value"]}`)}
	for _, input := range []string{`{"value":NaN}`, `{"value":Infinity}`, `{"value":1e309}`} {
		if err := ValidateJSONInput(definition, []byte(input)); err == nil {
			t.Fatalf("non-finite or overflowing number accepted: %s", input)
		}
	}
	if _, err := validateJSONObjectSchema(`{"type":"object","properties":{"value":{"type":"number","maximum":1e309}}}`); err == nil {
		t.Fatal("registry accepted an overflowing number boundary")
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
	for _, name := range []string{"core/plan.yaml", "core/context.yaml", "core/record.yaml", "core/history.yaml", "core/verify.yaml", "core/export.yaml", "core/import.yaml", "core/submit_execution_report.yaml", "packs/cross-zone/advisory.yaml", "packs/cross-zone/mirror.yaml", "packs/cross-zone/patch.yaml", "packs/cross-zone/audit.yaml"} {
		data, err := os.ReadFile(filepath.Join(source, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		writeDefinition(t, root, name, string(data))
	}
	return root
}

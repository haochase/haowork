package benchmark

import "testing"

func TestScenarioRequiresEvidenceFixtureAndSchemaDigest(t *testing.T) {
	s := Scenario{ID: "x", Version: "1", SourceBaseline: "b", ModelID: "m", TokenBudget: 1, TimeBudgetMS: 1, CompletionCriteria: []string{"done"}}
	if err := s.Validate(); err == nil {
		t.Fatal("expected required scenario metadata rejection")
	}
}

func TestExpectedSchemaRejectsUnknownPropertyPolicy(t *testing.T) {
	if err := ValidateExpectedSchema([]byte(`{"required":["a"],"properties":{"a":{"type":"string"}},"additionalProperties":true}`)); err == nil {
		t.Fatal("expected unknown property policy rejection")
	}
}

func TestReportSchemaEnforcesConstPatternAndMinItems(t *testing.T) {
	schema := []byte(`{"required":["report_version","scenario_sha256","raw_runs"],"properties":{"report_version":{"type":"string","const":"p0-05.v1"},"scenario_sha256":{"type":"string","pattern":"^[0-9a-f]{64}$"},"raw_runs":{"type":"array","minItems":1}},"additionalProperties":false}`)
	report := Report{ReportVersion: "wrong", ScenarioSHA256: "not-a-digest", RawRuns: nil}
	if err := ValidateReportAgainstSchema(report, schema); err == nil {
		t.Fatal("expected const/pattern/minItems rejection")
	}
}

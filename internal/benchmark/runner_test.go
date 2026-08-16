package benchmark

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func testScenario() Scenario {
	return Scenario{ID: "patch-delivery", Version: "1", SourceBaseline: "git:abc", ModelID: "fixture-model", TokenBudget: 1000, TimeBudgetMS: 60000, CompletionCriteria: []string{"audit", "patch"}, FixtureRepository: "fixtures/patch", RequiredEvidence: []string{"trace"}, SchemaSHA256: strings.Repeat("a", 64)}
}

func runFor(arm Arm, repetition int) RunRecord {
	return RunRecord{RunID: string(arm) + "-" + string(rune('0'+repetition)), Arm: arm, Repetition: repetition, AgentCount: arm.AgentCount(), HaoworkSkillsEnabled: arm.SkillsEnabled(), Metrics: Metrics{Success: true, TraceCompleteness: 1}, EvidenceRefs: []model.ArtifactRef{{Kind: "trace", URI: "sha256:trace", SHA256: strings.Repeat("a", 64)}}}
}

func TestBenchmarkRejectsArmWithDifferentBaselineModelBudgetOrCriteria(t *testing.T) {
	records := []RunRecord{runFor(ArmA, 1), runFor(ArmB, 1)}
	for _, record := range records {
		record.ScenarioSHA256 = "scenario"
		record.SourceBaseline = "baseline"
		record.ModelID = "model"
		record.TokenBudget = 10
		record.TimeBudgetMS = 20
		record.CompletionCriteria = []string{"done"}
	}
	records[0].ScenarioSHA256, records[0].SourceBaseline, records[0].ModelID, records[0].TokenBudget, records[0].TimeBudgetMS, records[0].CompletionCriteria = "scenario", "baseline", "model", 10, 20, []string{"done"}
	records[1].ScenarioSHA256, records[1].SourceBaseline, records[1].ModelID, records[1].TokenBudget, records[1].TimeBudgetMS, records[1].CompletionCriteria = "scenario-2", "baseline", "model", 10, 20, []string{"done"}
	if err := ValidateFairness(records); err == nil {
		t.Fatal("expected fairness error")
	}
}

func TestBenchmarkMapsAAndBToOneStandaloneWorkerAndCAndDToFullTeam(t *testing.T) {
	for _, arm := range []Arm{ArmA, ArmB, ArmC, ArmD} {
		if runFor(arm, 1).AgentCount != arm.AgentCount() {
			t.Fatalf("wrong agent count for %s", arm)
		}
	}
}

func TestBenchmarkEnablesHaoworkSkillsOnlyForBAndD(t *testing.T) {
	if ArmA.SkillsEnabled() || !ArmB.SkillsEnabled() || ArmC.SkillsEnabled() || !ArmD.SkillsEnabled() {
		t.Fatal("invalid skill mapping")
	}
}

func TestRunnerRequiresConfiguredExecutor(t *testing.T) {
	runner, err := NewRunner(testScenario(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), []Arm{ArmA}, 1)
	if err == nil || !strings.Contains(err.Error(), "real AgentTeams endpoint") {
		t.Fatalf("expected blocked executor, got %v", err)
	}
}

func TestValidateReleasePlanRequiresAllArmsAndThreeRepetitions(t *testing.T) {
	if err := ValidateReleasePlan([]Arm{ArmA, ArmB, ArmC, ArmD}, 3); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReleasePlan([]Arm{ArmA}, 3); err == nil {
		t.Fatal("expected all-arm rejection")
	}
	if err := ValidateReleasePlan([]Arm{ArmA, ArmB, ArmC, ArmD}, 2); err == nil {
		t.Fatal("expected repetition rejection")
	}
}

func TestValidateFairnessRejectsPartialOrShortRelease(t *testing.T) {
	records := []RunRecord{}
	for _, arm := range []Arm{ArmA, ArmB, ArmC, ArmD} {
		for n := 1; n <= 3; n++ {
			records = append(records, completeRunForFairness(arm, n))
		}
	}
	if err := ValidateFairness(records); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFairness(records[:11]); err == nil {
		t.Fatal("expected short release rejection")
	}
}

func TestRunnerDerivesMetricsOnlyFromSignedLedger(t *testing.T) {
	publicKey, privateKey, _ := ed25519.GenerateKey(nil)
	scenario := testScenario()
	scenario.RequiredEvidence = []string{"trace"}
	runner, err := NewRunnerWithEndpoint(scenario, func(_ context.Context, s Scenario, arm Arm, repetition int) (RunRecord, error) {
		ledger := LedgerSnapshot{Success: true, TraceEvents: 1, ExpectedTraceEvents: 1, TraceDigest: "trace", EvidenceRefs: []model.ArtifactRef{{Kind: "trace", URI: "trace", SHA256: strings.Repeat("a", 64)}}}
		hash, _ := LedgerDigest(ledger)
		record := RunRecord{RunID: string(arm) + "-" + string(rune('0'+repetition)), Arm: arm, Repetition: repetition, ScenarioSHA256: "", AgentCount: arm.AgentCount(), HaoworkSkillsEnabled: arm.SkillsEnabled(), AgentTeamsEndpoint: "https://example.test", StableProfile: "v1.2.2", AgentTeamsInstanceID: "instance", LedgerSnapshot: &ledger, LedgerHash: hash, Metrics: Metrics{PolicyViolations: 99}}
		record.ScenarioSHA256, _ = ScenarioDigest(s)
		payload, _ := AttestationPayload(record)
		record.AttestationSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
		return record, nil
	}, publicKey, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), []Arm{ArmA, ArmB, ArmC, ArmD}, 3)
	if err != nil {
		t.Fatal(err)
	}
}

func completeRunForFairness(arm Arm, n int) RunRecord {
	r := runFor(arm, n)
	r.ScenarioSHA256, r.SourceBaseline, r.ModelID, r.TokenBudget, r.TimeBudgetMS, r.CompletionCriteria = "scenario", "baseline", "model", 100, 200, []string{"done"}
	return r
}

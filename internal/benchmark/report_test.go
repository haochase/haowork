package benchmark

import (
	"strings"
	"testing"
)

func completeRun(arm Arm, n int) RunRecord {
	r := runFor(arm, n)
	r.ScenarioSHA256, r.SourceBaseline, r.ModelID, r.TokenBudget, r.TimeBudgetMS, r.CompletionCriteria = "scenario", "baseline", "model", 100, 200, []string{"done"}
	return r
}

func TestReportPreservesRawRunsAndNeverClaimsSignificanceFromOneSample(t *testing.T) {
	var records []RunRecord
	for _, arm := range []Arm{ArmA, ArmB, ArmC, ArmD} {
		for n := 1; n <= 3; n++ {
			records = append(records, completeRun(arm, n))
		}
	}
	report, err := BuildReport(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.RawRuns) != 12 || len(report.Claims) == 0 {
		t.Fatalf("raw run or factual claim missing: %+v", report)
	}
	for _, claim := range report.Claims {
		if strings.Contains(strings.ToLower(claim), "significant") && !strings.Contains(claim, "少于三次") {
			t.Fatal("unsupported significance claim")
		}
	}
}

func TestReportSchemaRejectsMissingEvidenceRefs(t *testing.T) {
	record := completeRun(ArmA, 1)
	record.EvidenceRefs = nil
	if _, err := BuildReport([]RunRecord{record}); err == nil {
		t.Fatal("expected successful run evidence rejection")
	}
}

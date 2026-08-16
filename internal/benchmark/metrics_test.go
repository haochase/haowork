package benchmark

import "testing"

func TestMetricsDerivePolicyEvidenceTraceAndRecoveryFromLedgers(t *testing.T) {
	metrics, err := DeriveMetrics(LedgerSnapshot{PolicyViolations: 1, InvalidEvidence: 2, TraceEvents: 3, ExpectedTraceEvents: 4, MigrationRecoveryMS: 90, Success: true})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.PolicyViolations != 1 || metrics.InvalidEvidence != 2 || metrics.TraceCompleteness != .75 || metrics.MigrationRecoveryMS != 90 {
		t.Fatalf("unexpected metrics: %+v", metrics)
	}
}

func TestMetricsRejectNegativeCounters(t *testing.T) {
	if _, err := DeriveMetrics(LedgerSnapshot{ToolCalls: -1}); err == nil {
		t.Fatal("expected negative counter rejection")
	}
}

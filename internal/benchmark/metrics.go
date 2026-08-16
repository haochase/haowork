package benchmark

import "errors"

func DeriveMetrics(snapshot LedgerSnapshot) (Metrics, error) {
	if snapshot.PolicyViolations < 0 || snapshot.InvalidEvidence < 0 || snapshot.ToolCalls < 0 ||
		snapshot.HumanInterventions < 0 || snapshot.InputTokens < 0 || snapshot.OutputTokens < 0 ||
		snapshot.DurationMS < 0 || snapshot.MigrationRecoveryMS < 0 || snapshot.DuplicateWorkItems < 0 ||
		snapshot.TraceEvents < 0 || snapshot.ExpectedTraceEvents < 0 || snapshot.FunctionalDefects < 0 ||
		snapshot.PerformanceRegressions < 0 {
		return Metrics{}, errors.New("ledger counters cannot be negative")
	}
	return snapshot.Metrics(), nil
}

func AggregateRuns(records []RunRecord) ([]Aggregate, error) {
	if err := ValidateFairness(records); err != nil {
		return nil, err
	}
	byArm := map[Arm][]RunRecord{}
	for _, record := range records {
		byArm[record.Arm] = append(byArm[record.Arm], record)
	}
	var aggregates []Aggregate
	for _, arm := range sortedArms() {
		runs := byArm[arm]
		if len(runs) == 0 {
			continue
		}
		a := Aggregate{Arm: arm, Repetitions: len(runs)}
		for _, run := range runs {
			if run.Metrics.Success {
				a.Successes++
			}
			a.MeanDurationMS += float64(run.Metrics.DurationMS)
			a.MeanMigrationRecoveryMS += float64(run.Metrics.MigrationRecoveryMS)
			a.MeanInputTokens += float64(run.Metrics.InputTokens)
			a.MeanOutputTokens += float64(run.Metrics.OutputTokens)
			a.MeanTraceCompleteness += run.Metrics.TraceCompleteness
		}
		divisor := float64(len(runs))
		a.SuccessRate = float64(a.Successes) / divisor
		a.MeanDurationMS /= divisor
		a.MeanMigrationRecoveryMS /= divisor
		a.MeanInputTokens /= divisor
		a.MeanOutputTokens /= divisor
		a.MeanTraceCompleteness /= divisor
		aggregates = append(aggregates, a)
	}
	return aggregates, nil
}

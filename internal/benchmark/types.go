package benchmark

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"go.yaml.in/yaml/v3"
)

type Arm string

const (
	ArmA Arm = "A"
	ArmB Arm = "B"
	ArmC Arm = "C"
	ArmD Arm = "D"
)

func (a Arm) Valid() bool { return a == ArmA || a == ArmB || a == ArmC || a == ArmD }

func (a Arm) SkillsEnabled() bool { return a == ArmB || a == ArmD }

func (a Arm) AgentCount() int {
	if a == ArmA || a == ArmB {
		return 1
	}
	return 5
}

type Scenario struct {
	ID                 string   `yaml:"id" json:"id"`
	Version            string   `yaml:"version" json:"version"`
	SourceBaseline     string   `yaml:"source_baseline" json:"source_baseline"`
	ModelID            string   `yaml:"model_id" json:"model_id"`
	TokenBudget        int64    `yaml:"token_budget" json:"token_budget"`
	TimeBudgetMS       int64    `yaml:"time_budget_ms" json:"time_budget_ms"`
	CompletionCriteria []string `yaml:"completion_criteria" json:"completion_criteria"`
	FixtureRepository  string   `yaml:"fixture_repository" json:"fixture_repository"`
	RequiredEvidence   []string `yaml:"required_evidence" json:"required_evidence"`
	SchemaSHA256       string   `yaml:"schema_sha256" json:"schema_sha256"`
}

func LoadScenario(data []byte) (Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Scenario{}, fmt.Errorf("decode scenario: %w", err)
	}
	if err := s.Validate(); err != nil {
		return Scenario{}, err
	}
	return s, nil
}

func (s Scenario) Validate() error {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Version) == "" {
		return errors.New("scenario id and version are required")
	}
	if strings.TrimSpace(s.SourceBaseline) == "" || strings.TrimSpace(s.ModelID) == "" {
		return errors.New("scenario source_baseline and model_id are required")
	}
	if s.TokenBudget <= 0 || s.TimeBudgetMS <= 0 {
		return errors.New("scenario budgets must be positive")
	}
	if len(s.CompletionCriteria) == 0 {
		return errors.New("scenario completion_criteria must not be empty")
	}
	if strings.TrimSpace(s.FixtureRepository) == "" || len(s.RequiredEvidence) == 0 {
		return errors.New("scenario fixture_repository and required_evidence are required")
	}
	if !validDigest(s.SchemaSHA256) {
		return errors.New("scenario schema_sha256 must be a SHA-256 digest")
	}
	return nil
}

type Metrics struct {
	Success                bool    `json:"success"`
	PolicyViolations       int     `json:"policy_violations"`
	InvalidEvidence        int     `json:"invalid_evidence"`
	ToolCalls              int     `json:"tool_calls"`
	HumanInterventions     int     `json:"human_interventions"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	DurationMS             int64   `json:"duration_ms"`
	MigrationRecoveryMS    int64   `json:"migration_recovery_ms"`
	DuplicateWorkItems     int     `json:"duplicate_work_items"`
	TraceCompleteness      float64 `json:"trace_completeness"`
	FunctionalDefects      int     `json:"functional_defects"`
	PerformanceRegressions int     `json:"performance_regressions"`
}

func (m Metrics) Validate() error {
	if m.PolicyViolations < 0 || m.InvalidEvidence < 0 || m.ToolCalls < 0 || m.HumanInterventions < 0 ||
		m.InputTokens < 0 || m.OutputTokens < 0 || m.DurationMS < 0 || m.MigrationRecoveryMS < 0 ||
		m.DuplicateWorkItems < 0 || m.FunctionalDefects < 0 || m.PerformanceRegressions < 0 {
		return errors.New("metrics counters cannot be negative")
	}
	if m.TraceCompleteness < 0 || m.TraceCompleteness > 1 {
		return errors.New("trace completeness must be between 0 and 1")
	}
	return nil
}

type RunRecord struct {
	RunID                string              `json:"run_id"`
	Arm                  Arm                 `json:"arm"`
	Repetition           int                 `json:"repetition"`
	ScenarioSHA256       string              `json:"scenario_sha256"`
	SourceBaseline       string              `json:"source_baseline"`
	ModelID              string              `json:"model_id"`
	TokenBudget          int64               `json:"token_budget"`
	TimeBudgetMS         int64               `json:"time_budget_ms"`
	CompletionCriteria   []string            `json:"completion_criteria"`
	AgentCount           int                 `json:"agent_count"`
	HaoworkSkillsEnabled bool                `json:"haowork_skills_enabled"`
	Environment          string              `json:"environment"`
	Metrics              Metrics             `json:"metrics"`
	EvidenceRefs         []model.ArtifactRef `json:"evidence_refs"`
	LedgerSnapshot       *LedgerSnapshot     `json:"ledger_snapshot,omitempty"`
	LedgerHash           string              `json:"ledger_hash,omitempty"`
	StableProfile        string              `json:"stable_profile"`
	AgentTeamsInstanceID string              `json:"agentteams_instance_id"`
	AgentTeamsEndpoint   string              `json:"agentteams_endpoint"`
	AttestationSignature string              `json:"attestation_signature"`
	Blocked              bool                `json:"blocked,omitempty"`
	BlockedReason        string              `json:"blocked_reason,omitempty"`
}

func (r RunRecord) Validate() error {
	if strings.TrimSpace(r.RunID) == "" || !r.Arm.Valid() || r.Repetition <= 0 {
		return errors.New("run_id, valid arm and positive repetition are required")
	}
	if r.AgentCount != r.Arm.AgentCount() {
		return fmt.Errorf("arm %s requires %d agents, got %d", r.Arm, r.Arm.AgentCount(), r.AgentCount)
	}
	if r.HaoworkSkillsEnabled != r.Arm.SkillsEnabled() {
		return fmt.Errorf("arm %s has invalid Haowork skill setting", r.Arm)
	}
	if err := r.Metrics.Validate(); err != nil {
		return err
	}
	if r.Metrics.Success && len(r.EvidenceRefs) == 0 {
		return errors.New("successful run must include evidence_refs")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type LedgerSnapshot struct {
	PolicyViolations       int
	InvalidEvidence        int
	ToolCalls              int
	HumanInterventions     int
	InputTokens            int64
	OutputTokens           int64
	DurationMS             int64
	MigrationRecoveryMS    int64
	DuplicateWorkItems     int
	TraceEvents            int
	ExpectedTraceEvents    int
	FunctionalDefects      int
	PerformanceRegressions int
	Success                bool                `json:"success"`
	TraceDigest            string              `json:"trace_digest"`
	EvidenceRefs           []model.ArtifactRef `json:"evidence_refs"`
}

func (s LedgerSnapshot) Metrics() Metrics {
	completeness := 0.0
	if s.ExpectedTraceEvents > 0 {
		completeness = float64(s.TraceEvents) / float64(s.ExpectedTraceEvents)
		if completeness > 1 {
			completeness = 1
		}
	}
	return Metrics{Success: s.Success, PolicyViolations: s.PolicyViolations, InvalidEvidence: s.InvalidEvidence,
		ToolCalls: s.ToolCalls, HumanInterventions: s.HumanInterventions, InputTokens: s.InputTokens,
		OutputTokens: s.OutputTokens, DurationMS: s.DurationMS, MigrationRecoveryMS: s.MigrationRecoveryMS,
		DuplicateWorkItems: s.DuplicateWorkItems, TraceCompleteness: completeness,
		FunctionalDefects: s.FunctionalDefects, PerformanceRegressions: s.PerformanceRegressions}
}

func (s LedgerSnapshot) Validate() error {
	if _, err := DeriveMetrics(s); err != nil {
		return err
	}
	if s.ExpectedTraceEvents > 0 && strings.TrimSpace(s.TraceDigest) == "" {
		return errors.New("ledger trace_digest is required")
	}
	return nil
}

type Aggregate struct {
	Arm                     Arm     `json:"arm"`
	Repetitions             int     `json:"repetitions"`
	Successes               int     `json:"successes"`
	SuccessRate             float64 `json:"success_rate"`
	MeanDurationMS          float64 `json:"mean_duration_ms"`
	MeanMigrationRecoveryMS float64 `json:"mean_migration_recovery_ms"`
	MeanInputTokens         float64 `json:"mean_input_tokens"`
	MeanOutputTokens        float64 `json:"mean_output_tokens"`
	MeanTraceCompleteness   float64 `json:"mean_trace_completeness"`
}

type Report struct {
	ReportVersion       string              `json:"report_version"`
	ScenarioSHA256      string              `json:"scenario_sha256"`
	GeneratedAt         string              `json:"generated_at"`
	RawRuns             []RunRecord         `json:"raw_runs"`
	Aggregates          []Aggregate         `json:"aggregates"`
	EvidenceRefs        []model.ArtifactRef `json:"evidence_refs"`
	Claims              []string            `json:"claims"`
	RawRunsSHA256       string              `json:"raw_runs_sha256"`
	RawRunsPath         string              `json:"raw_runs_path"`
	EnvironmentVersions map[string]string   `json:"environment_versions"`
}

func sortedArms() []Arm { return []Arm{ArmA, ArmB, ArmC, ArmD} }

func normalizeCriteria(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

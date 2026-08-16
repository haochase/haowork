package benchmark

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/model"
)

var ErrExecutorUnavailable = errors.New("benchmark executor is not configured; real AgentTeams endpoint is required")

type Executor func(context.Context, Scenario, Arm, int) (RunRecord, error)

type Runner struct {
	Scenario       Scenario
	ScenarioSHA256 string
	Executor       Executor
	PublicKey      ed25519.PublicKey
	Endpoint       string
}

func ScenarioDigest(s Scenario) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func LedgerDigest(snapshot LedgerSnapshot) (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func NewRunner(s Scenario, executor Executor) (*Runner, error) {
	return NewRunnerWithVerifier(s, executor, nil)
}

func NewRunnerWithVerifier(s Scenario, executor Executor, publicKey ed25519.PublicKey) (*Runner, error) {
	return NewRunnerWithEndpoint(s, executor, publicKey, "")
}

func NewRunnerWithEndpoint(s Scenario, executor Executor, publicKey ed25519.PublicKey, endpoint string) (*Runner, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	digest, err := ScenarioDigest(s)
	if err != nil {
		return nil, err
	}
	return &Runner{Scenario: s, ScenarioSHA256: digest, Executor: executor, PublicKey: publicKey, Endpoint: endpoint}, nil
}

type attestationEnvelope struct {
	StableProfile        string `json:"stable_profile"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	AgentTeamsEndpoint   string `json:"agentteams_endpoint"`
	ScenarioSHA256       string `json:"scenario_sha256"`
	Arm                  Arm    `json:"arm"`
	Repetition           int    `json:"repetition"`
	AgentCount           int    `json:"agent_count"`
	HaoworkSkillsEnabled bool   `json:"haowork_skills_enabled"`
	LedgerHash           string `json:"ledger_hash"`
}

func AttestationPayload(record RunRecord) ([]byte, error) {
	return json.Marshal(attestationEnvelope{StableProfile: record.StableProfile, AgentTeamsInstanceID: record.AgentTeamsInstanceID, AgentTeamsEndpoint: record.AgentTeamsEndpoint,
		ScenarioSHA256: record.ScenarioSHA256, Arm: record.Arm, Repetition: record.Repetition, AgentCount: record.AgentCount, HaoworkSkillsEnabled: record.HaoworkSkillsEnabled, LedgerHash: record.LedgerHash})
}

func VerifyAttestation(record RunRecord, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("benchmark attestation verifier is not configured")
	}
	if record.StableProfile != agentteamsbridge.StableVersion || strings.TrimSpace(record.AgentTeamsInstanceID) == "" {
		return fmt.Errorf("attestation requires stable AgentTeams %s profile and instance id", agentteamsbridge.StableVersion)
	}
	sig, err := base64.StdEncoding.DecodeString(record.AttestationSignature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("invalid attestation signature")
	}
	payload, err := AttestationPayload(record)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, payload, sig) {
		return errors.New("attestation signature verification failed")
	}
	return nil
}

func (r *Runner) Run(ctx context.Context, arms []Arm, repetitions int) ([]RunRecord, error) {
	if r == nil {
		return nil, errors.New("nil benchmark runner")
	}
	if repetitions < 1 {
		return nil, errors.New("repetitions must be positive")
	}
	if r.Executor == nil {
		return nil, ErrExecutorUnavailable
	}
	if len(r.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("benchmark blocked: attestation verifier is not configured")
	}
	if len(arms) == 0 {
		arms = sortedArms()
	}
	seen := map[Arm]bool{}
	for _, arm := range arms {
		if !arm.Valid() || seen[arm] {
			return nil, fmt.Errorf("invalid or duplicate arm %q", arm)
		}
		seen[arm] = true
	}
	var records []RunRecord
	for _, arm := range arms {
		for repetition := 1; repetition <= repetitions; repetition++ {
			record, err := r.Executor(ctx, r.Scenario, arm, repetition)
			if err != nil {
				return nil, fmt.Errorf("arm %s repetition %d: %w", arm, repetition, err)
			}
			if record.ScenarioSHA256 != r.ScenarioSHA256 || record.Arm != arm || record.Repetition != repetition {
				return nil, fmt.Errorf("arm %s repetition %d returned mismatched attestation binding", arm, repetition)
			}
			if err := VerifyAttestation(record, r.PublicKey); err != nil {
				return nil, err
			}
			if r.Endpoint == "" || record.AgentTeamsEndpoint != r.Endpoint || record.AgentCount != arm.AgentCount() || record.HaoworkSkillsEnabled != arm.SkillsEnabled() {
				return nil, fmt.Errorf("arm %s repetition %d returned mismatched endpoint or treatment binding", arm, repetition)
			}
			record.SourceBaseline = r.Scenario.SourceBaseline
			record.ModelID = r.Scenario.ModelID
			record.TokenBudget = r.Scenario.TokenBudget
			record.TimeBudgetMS = r.Scenario.TimeBudgetMS
			record.CompletionCriteria = normalizeCriteria(r.Scenario.CompletionCriteria)
			if record.LedgerSnapshot == nil {
				return nil, errors.New("executor must return a controlled ledger_snapshot")
			}
			if err := record.LedgerSnapshot.Validate(); err != nil {
				return nil, fmt.Errorf("arm %s repetition %d ledger: %w", arm, repetition, err)
			}
			ledgerHash, err := LedgerDigest(*record.LedgerSnapshot)
			if err != nil {
				return nil, err
			}
			if record.LedgerHash == "" || record.LedgerHash != ledgerHash {
				return nil, fmt.Errorf("arm %s repetition %d ledger hash mismatch", arm, repetition)
			}
			record.Metrics, err = DeriveMetrics(*record.LedgerSnapshot)
			if err != nil {
				return nil, err
			}
			record.EvidenceRefs = append([]model.ArtifactRef(nil), record.LedgerSnapshot.EvidenceRefs...)
			if record.Metrics.Success && len(record.EvidenceRefs) == 0 {
				return nil, fmt.Errorf("arm %s repetition %d successful ledger has no evidence", arm, repetition)
			}
			if record.Metrics.Success && record.Metrics.TraceCompleteness < 1 {
				return nil, fmt.Errorf("arm %s repetition %d successful ledger has incomplete trace", arm, repetition)
			}
			for _, required := range r.Scenario.RequiredEvidence {
				found := false
				for _, ref := range record.EvidenceRefs {
					if ref.Kind == required {
						found = true
						break
					}
				}
				if !found && record.Metrics.Success {
					return nil, fmt.Errorf("arm %s repetition %d missing required evidence %q", arm, repetition, required)
				}
			}
			if err := record.Validate(); err != nil {
				return nil, err
			}
			records = append(records, record)
		}
	}
	if err := ValidateFairness(records); err != nil {
		return nil, err
	}
	return records, nil
}

func ValidateFairness(records []RunRecord) error {
	if len(records) == 0 {
		return errors.New("no benchmark runs")
	}
	first := records[0]
	criteria := normalizeCriteria(first.CompletionCriteria)
	counts := map[Arm]int{}
	runIDs := map[string]bool{}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return err
		}
		if record.ScenarioSHA256 != first.ScenarioSHA256 || record.SourceBaseline != first.SourceBaseline || record.ModelID != first.ModelID ||
			record.TokenBudget != first.TokenBudget || record.TimeBudgetMS != first.TimeBudgetMS ||
			fmt.Sprint(normalizeCriteria(record.CompletionCriteria)) != fmt.Sprint(criteria) {
			return errors.New("benchmark arms must use the same baseline, model, budgets and completion criteria")
		}
		counts[record.Arm]++
		if runIDs[record.RunID] {
			return fmt.Errorf("duplicate run_id %q", record.RunID)
		}
		runIDs[record.RunID] = true
	}
	if len(counts) != 4 {
		return errors.New("benchmark requires all arms A,B,C,D")
	}
	for _, arm := range sortedArms() {
		if counts[arm] < 3 {
			return fmt.Errorf("arm %s requires at least three repetitions", arm)
		}
	}
	return nil
}

func ValidateReleasePlan(arms []Arm, repetitions int) error {
	if repetitions < 3 {
		return errors.New("release benchmark requires at least three repetitions per arm")
	}
	if len(arms) != 4 {
		return errors.New("release benchmark requires exactly arms A,B,C,D")
	}
	seen := map[Arm]bool{}
	for _, arm := range arms {
		if !arm.Valid() || seen[arm] {
			return errors.New("release benchmark requires exactly one of each arm A,B,C,D")
		}
		seen[arm] = true
	}
	for _, arm := range sortedArms() {
		if !seen[arm] {
			return errors.New("release benchmark requires exactly one of each arm A,B,C,D")
		}
	}
	return nil
}

func ValidateReleaseRuns(records []RunRecord) error {
	return ValidateFairness(records)
}

func ReadRunRecords(path string) ([]RunRecord, error) {
	records, _, err := ReadRunRecordsBytes(path)
	return records, err
}

func ReadRunRecordsBytes(path string) ([]RunRecord, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var records []RunRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, nil, fmt.Errorf("decode raw runs: %w", err)
	}
	if err := ValidateFairness(records); err != nil {
		return nil, nil, err
	}
	return records, b, nil
}

func SortRuns(records []RunRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].Arm != records[j].Arm {
			return records[i].Arm < records[j].Arm
		}
		return records[i].Repetition < records[j].Repetition
	})
}

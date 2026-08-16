package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func BuildReport(records []RunRecord) (Report, error) {
	if err := ValidateFairness(records); err != nil {
		return Report{}, err
	}
	aggregates, err := AggregateRuns(records)
	if err != nil {
		return Report{}, err
	}
	SortRuns(records)
	rawBytes, err := json.Marshal(records)
	if err != nil {
		return Report{}, err
	}
	hash := sha256.Sum256(rawBytes)
	report := Report{ReportVersion: "p0-05.v1", ScenarioSHA256: records[0].ScenarioSHA256,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), RawRuns: append([]RunRecord(nil), records...), Aggregates: aggregates,
		Claims: []string{"本报告保留原始运行记录；单次或少于三次重复实验不支持显著性结论。"}, RawRunsSHA256: hex.EncodeToString(hash[:]),
		RawRunsPath: "raw-runs.json", EnvironmentVersions: map[string]string{"go": runtime.Version()}}
	report.EvidenceRefs = EvidenceRefsFromRuns(records)
	return report, nil
}

func BuildReportFromRawBytes(raw []byte) (Report, error) {
	return BuildReportFromRawBytesAt(raw, "raw-runs.json")
}

func BuildReportFromRawBytesAt(raw []byte, rawPath string) (Report, error) {
	var records []RunRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return Report{}, fmt.Errorf("decode raw runs: %w", err)
	}
	report, err := BuildReport(records)
	if err != nil {
		return Report{}, err
	}
	hash := sha256.Sum256(raw)
	report.RawRunsSHA256 = hex.EncodeToString(hash[:])
	report.RawRunsPath = rawPath
	return report, nil
}

func ValidateReport(report Report) error {
	if report.ReportVersion == "" || report.ScenarioSHA256 == "" || !validDigest(report.RawRunsSHA256) || report.RawRunsPath == "" || len(report.RawRuns) == 0 {
		return errors.New("report version, scenario digest, raw path/hash and raw runs are required")
	}
	if err := ValidateFairness(report.RawRuns); err != nil {
		return err
	}
	if len(report.EvidenceRefs) == 0 {
		return errors.New("report requires evidence_refs from immutable raw records")
	}
	allowed := map[string]bool{}
	for _, ref := range EvidenceRefsFromRuns(report.RawRuns) {
		allowed[ref.Kind+"\x00"+ref.URI+"\x00"+ref.SHA256] = true
	}
	for _, ref := range report.EvidenceRefs {
		if ref.Kind == "" || ref.URI == "" || !validDigest(ref.SHA256) {
			return fmt.Errorf("invalid evidence ref %q", ref.URI)
		}
		if !allowed[ref.Kind+"\x00"+ref.URI+"\x00"+ref.SHA256] {
			return errors.New("report evidence_refs must come from raw runs")
		}
	}
	return nil
}

func MarshalReport(report Report) ([]byte, error) {
	if err := ValidateReport(report); err != nil {
		return nil, err
	}
	return json.MarshalIndent(report, "", "  ")
}

func EvidenceRefsFromRuns(records []RunRecord) []model.ArtifactRef {
	seen := map[string]bool{}
	var refs []model.ArtifactRef
	for _, record := range records {
		for _, ref := range record.EvidenceRefs {
			key := ref.Kind + "\x00" + ref.URI + "\x00" + ref.SHA256
			if !seen[key] {
				seen[key] = true
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

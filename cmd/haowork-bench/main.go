package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haochase/haowork/internal/benchmark"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: haowork-bench run|report")
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCommand(os.Args[2:])
	case "report":
		err = reportCommand(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) { fmt.Fprintln(os.Stderr, "haowork-bench: "+message); os.Exit(1) }

func runCommand(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	scenarioPath := flags.String("scenario", "bench/p0-05/scenario.yaml", "scenario YAML")
	armsFlag := flags.String("arms", "A,B,C,D", "comma-separated arms")
	repetitions := flags.Int("repetitions", 3, "repetitions per arm")
	output := flags.String("output", ".haowork/cache/bench/p0-05", "output directory")
	executorCommand := flags.String("executor-command", os.Getenv("HAOWORK_P005_BENCHMARK_COMMAND"), "command that runs one real AgentTeams arm")
	publicKeyValue := flags.String("public-key", os.Getenv("HAOWORK_P005_BENCHMARK_PUBLIC_KEY"), "base64 Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*scenarioPath)
	if err != nil {
		return err
	}
	scenario, err := benchmark.LoadScenario(data)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*executorCommand) == "" {
		return benchmark.ErrExecutorUnavailable
	}
	if strings.TrimSpace(os.Getenv("HAOWORK_P005_BENCHMARK_ENDPOINT")) == "" {
		return errors.New("benchmark blocked: real AgentTeams endpoint is not configured")
	}
	expectedSchema, err := os.ReadFile(filepath.Join(filepath.Dir(*scenarioPath), "expected-schema.json"))
	if err != nil {
		return err
	}
	if err := benchmark.ValidateScenarioSchema(scenario, expectedSchema); err != nil {
		return err
	}
	var arms []benchmark.Arm
	for _, raw := range strings.Split(*armsFlag, ",") {
		arm := benchmark.Arm(strings.TrimSpace(raw))
		if !arm.Valid() {
			return fmt.Errorf("invalid arm %q", raw)
		}
		arms = append(arms, arm)
	}
	if err := benchmark.ValidateReleasePlan(arms, *repetitions); err != nil {
		return err
	}
	keyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*publicKeyValue))
	if err != nil || len(keyBytes) != ed25519.PublicKeySize {
		return errors.New("benchmark blocked: valid Ed25519 public key is required")
	}
	publicKey := ed25519.PublicKey(keyBytes)
	executor := func(ctx context.Context, s benchmark.Scenario, arm benchmark.Arm, repetition int) (benchmark.RunRecord, error) {
		parts := strings.Fields(*executorCommand)
		if len(parts) == 0 {
			return benchmark.RunRecord{}, errors.New("empty benchmark command")
		}
		cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
		cmd.Env = append(os.Environ(), "HAOWORK_BENCH_ARM="+string(arm), "HAOWORK_BENCH_REPETITION="+strconv.Itoa(repetition))
		out, err := cmd.Output()
		if err != nil {
			return benchmark.RunRecord{}, fmt.Errorf("real executor failed: %w", err)
		}
		var record benchmark.RunRecord
		if err := json.Unmarshal(out, &record); err != nil {
			return benchmark.RunRecord{}, fmt.Errorf("executor returned invalid run record: %w", err)
		}
		return record, nil
	}
	runner, err := benchmark.NewRunnerWithEndpoint(scenario, executor, publicKey, os.Getenv("HAOWORK_P005_BENCHMARK_ENDPOINT"))
	if err != nil {
		return err
	}
	records, err := runner.Run(context.Background(), arms, *repetitions)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomicExclusive(filepath.Join(*output, "raw-runs.json"), encoded)
}

func reportCommand(args []string) error {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	input := flags.String("input", ".haowork/cache/bench/p0-05", "raw run directory")
	output := flags.String("output", ".haowork/cache/bench/p0-05/report.json", "report path")
	schemaPath := flags.String("schema", "bench/p0-05/expected-schema.json", "expected report schema")
	publicKeyValue := flags.String("public-key", os.Getenv("HAOWORK_P005_BENCHMARK_PUBLIC_KEY"), "base64 Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	records, rawBytes, err := benchmark.ReadRunRecordsBytes(filepath.Join(*input, "raw-runs.json"))
	if err != nil {
		return err
	}
	if err := benchmark.ValidateReleaseRuns(records); err != nil {
		return err
	}
	keyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*publicKeyValue))
	if err != nil || len(keyBytes) != ed25519.PublicKeySize {
		return errors.New("benchmark blocked: valid Ed25519 public key is required")
	}
	for _, record := range records {
		if err := benchmark.VerifyAttestation(record, ed25519.PublicKey(keyBytes)); err != nil {
			return err
		}
	}
	report, err := benchmark.BuildReportFromRawBytesAt(rawBytes, filepath.Join(*input, "raw-runs.json"))
	if err != nil {
		return err
	}
	expectedSchema, err := os.ReadFile(*schemaPath)
	if err != nil {
		return err
	}
	if err := benchmark.ValidateReportAgainstSchema(report, expectedSchema); err != nil {
		return err
	}
	encoded, err := benchmark.MarshalReport(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		return err
	}
	return writeAtomicExclusive(*output, encoded)
}

func writeAtomicExclusive(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing evidence file %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".haowork-evidence-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Hard-link publication is exclusive on Windows and Unix: it cannot replace
	// an existing destination, unlike os.Rename on Unix.
	if err := os.Link(tmpName, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("refusing to overwrite existing evidence file %s", path)
		}
		return err
	}
	return os.Remove(tmpName)
}

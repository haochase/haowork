package agentteamsbridge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestOfficialContractPinsV122TagCommitAndChartMetadata(t *testing.T) {
	contract, err := agentteamsbridge.LoadOfficialContract(officialContractPath(t))
	if err != nil {
		t.Fatalf("load official contract: %v", err)
	}
	if contract.Repository != "https://github.com/agentscope-ai/AgentTeams" {
		t.Fatalf("repository = %q", contract.Repository)
	}
	if contract.Tag != "v1.2.2" {
		t.Fatalf("tag = %q", contract.Tag)
	}
	if contract.Commit != "849182af8e017168a5a200a87b1062142caf462d" {
		t.Fatalf("commit = %q", contract.Commit)
	}
	if contract.ChartPath != "helm/agentteams" || contract.ChartVersion != "1.1.1" || contract.ChartAppVersion != "1.1.1" {
		t.Fatalf("chart metadata = %#v", contract)
	}
	if contract.ImageResolution.Status != agentteamsbridge.ImageResolutionResolved {
		t.Fatalf("image resolution status = %q", contract.ImageResolution.Status)
	}
	if len(contract.ImageResolution.Images) == 0 {
		t.Fatal("image lock is empty")
	}
	if !contract.DeploymentReady() {
		t.Fatal("active OpenClaw deployment profile is not ready")
	}
	if !contract.SupportsWorkerRuntime("openclaw") {
		t.Fatal("resolved OpenClaw worker runtime was rejected")
	}
	if contract.SupportsWorkerRuntime("openhuman") {
		t.Fatal("unpublished OpenHuman worker runtime was accepted")
	}
}

func TestOfficialContractUsesAgentTeamsV1Beta1Kinds(t *testing.T) {
	contract, err := agentteamsbridge.LoadOfficialContract(officialContractPath(t))
	if err != nil {
		t.Fatalf("load official contract: %v", err)
	}
	if contract.APIGroup != "agentteams.io" || contract.APIVersion != "v1beta1" {
		t.Fatalf("api = %s/%s", contract.APIGroup, contract.APIVersion)
	}
	for _, kind := range []string{"Manager", "Worker", "Team", "Human"} {
		if !contract.HasKind(kind) {
			t.Fatalf("contract does not include %s: %#v", kind, contract.Kinds)
		}
	}
}

func TestOfficialContractRejectsLegacyHiClawProfile(t *testing.T) {
	encoded, err := os.ReadFile(officialContractPath(t))
	if err != nil {
		t.Fatalf("read official contract: %v", err)
	}
	legacy := []byte(strings.Replace(string(encoded), `"api_group": "agentteams.io"`, `"api_group": "hiclaw.io"`, 1))
	if _, err := agentteamsbridge.ParseOfficialContract(legacy); err == nil || !strings.Contains(err.Error(), "API group/version") {
		t.Fatalf("legacy HiClaw profile error = %v", err)
	}
}

func TestOfficialContractRejectsAuditedImageTagDrift(t *testing.T) {
	encoded, err := os.ReadFile(officialContractPath(t))
	if err != nil {
		t.Fatalf("read official contract: %v", err)
	}
	var lock map[string]any
	if err := json.Unmarshal(encoded, &lock); err != nil {
		t.Fatalf("decode official contract fixture: %v", err)
	}
	resolution := lock["image_resolution"].(map[string]any)
	resolution["images"].([]any)[0].(map[string]any)["tag"] = "20260217"
	drifted, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("encode drifted image fixture: %v", err)
	}
	if _, err := agentteamsbridge.ParseOfficialContract(drifted); err == nil {
		t.Fatal("image tag drift was accepted")
	}
}

func TestOfficialContractRejectsResolvedDirectImagesWithoutFullRenderedInventory(t *testing.T) {
	encoded, err := os.ReadFile(officialContractPath(t))
	if err != nil {
		t.Fatalf("read official contract: %v", err)
	}
	var lock map[string]any
	if err := json.Unmarshal(encoded, &lock); err != nil {
		t.Fatalf("decode official contract fixture: %v", err)
	}
	resolution := lock["image_resolution"].(map[string]any)
	rendered := resolution["rendered_inventory"].(map[string]any)
	rendered["images"] = []any{}
	resolved, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("encode resolved direct image fixture: %v", err)
	}
	if _, err := agentteamsbridge.ParseOfficialContract(resolved); err == nil || !strings.Contains(err.Error(), "rendered image inventory") {
		t.Fatalf("resolved direct image inventory error = %v", err)
	}
}

func TestOfficialContractRejectsUnavailableRuntimeWhenSelectedForDeployment(t *testing.T) {
	contract, err := agentteamsbridge.LoadOfficialContract(officialContractPath(t))
	if err != nil {
		t.Fatalf("load official contract: %v", err)
	}
	if err := contract.ValidateDeploymentProfile("openclaw", "openhuman"); err == nil || !strings.Contains(err.Error(), "worker-openhuman") {
		t.Fatalf("OpenHuman deployment profile error = %v", err)
	}
}

func TestOfficialContractRejectsUpstreamManifestHashDrift(t *testing.T) {
	encoded, err := os.ReadFile(officialContractPath(t))
	if err != nil {
		t.Fatalf("read official contract: %v", err)
	}
	drifted := []byte(strings.Replace(string(encoded), "5c7b1b8d0968db7b452049e27e012b9668b38143b4236dea6b139e8f0467a18e", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1))
	if _, err := agentteamsbridge.ParseOfficialContract(drifted); err == nil || !strings.Contains(err.Error(), "upstream manifest") {
		t.Fatalf("upstream manifest drift error = %v", err)
	}
}

func officialContractPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "agentteams", "v1.2.2", "upstream.lock.json"))
}

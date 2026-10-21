//go:build agentteams_e2e

package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/model"
)

// TestP005RealAgentTeamsStructuredReceipt is intentionally an explicit real-
// infrastructure test. It must never silently fall back to a fake transport.
func TestP005RealAgentTeamsStructuredReceipt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env, err := loadP005Environment()
	if err != nil {
		t.Skipf("NOT_RUN: official AgentTeams E2E environment is unavailable: %v", err)
	}
	mission := p005Mission(env.EnvironmentID)
	input := corebridge.StartRequest{Mission: mission, Request: executorStartRequest(mission, env.RunID, env.ManagerPrincipalID, env.AgentTeamsInstanceID)}
	client, err := corebridge.NewClient(env.CoreBridgeURL, env.CoreBridgeToken, &http.Client{Timeout: 4 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if existing, err := client.Evidence(ctx, input.Request.RunID); err == nil {
		t.Fatalf("HAOWORK_P005_RUN_ID already has evidence and cannot be reused: %#v", existing)
	} else if !errors.Is(err, corebridge.ErrEvidenceNotFound) {
		t.Fatalf("preflight fresh RunID evidence: %v", err)
	}
	started, err := client.StartNew(ctx, input)
	if err != nil {
		t.Fatalf("start real AgentTeams run through cluster Core Bridge: %v", err)
	}
	persisted, err := client.Evidence(ctx, input.Request.RunID)
	if err != nil {
		t.Fatalf("read persisted execution receipt evidence: %v", err)
	}
	if len(started.ExecutionReceiptIDs) != 1 || len(persisted.ExecutionReceiptIDs) != 1 || started.ExecutionReceiptIDs[0] != persisted.ExecutionReceiptIDs[0] || started.SourceEventIDs[0] != persisted.ExecutionReceiptIDs[0] {
		t.Fatalf("Core Bridge evidence diverged: started=%#v persisted=%#v", started, persisted)
	}
}

type p005Environment struct {
	CoreBridgeURL, CoreBridgeToken, EnvironmentID, RunID string
	ManagerPrincipalID, AgentTeamsInstanceID             string
	ExpectExternalReceipt                                string
}

func loadP005Environment() (p005Environment, error) {
	values := p005Environment{
		CoreBridgeURL: os.Getenv("HAOWORK_P005_CORE_BRIDGE_URL"), CoreBridgeToken: os.Getenv("HAOWORK_P005_CORE_BRIDGE_TOKEN"),
		EnvironmentID: os.Getenv("HAOWORK_P005_ENVIRONMENT_ID"), RunID: os.Getenv("HAOWORK_P005_RUN_ID"), ManagerPrincipalID: os.Getenv("HAOWORK_P005_MANAGER_PRINCIPAL_ID"),
		AgentTeamsInstanceID: os.Getenv("HAOWORK_P005_AGENTTEAMS_INSTANCE_ID"), ExpectExternalReceipt: os.Getenv("HAOWORK_P005_EXPECT_EXTERNAL_RECEIPT"),
	}
	for name, value := range map[string]string{
		"HAOWORK_P005_CORE_BRIDGE_URL": values.CoreBridgeURL, "HAOWORK_P005_CORE_BRIDGE_TOKEN": values.CoreBridgeToken,
		"HAOWORK_P005_ENVIRONMENT_ID": values.EnvironmentID, "HAOWORK_P005_RUN_ID": values.RunID, "HAOWORK_P005_MANAGER_PRINCIPAL_ID": values.ManagerPrincipalID,
		"HAOWORK_P005_AGENTTEAMS_INSTANCE_ID": values.AgentTeamsInstanceID,
	} {
		if strings.TrimSpace(value) == "" {
			return p005Environment{}, fmt.Errorf("%s is required; real dual-environment deployment is not configured", name)
		}
	}
	if !strings.EqualFold(strings.TrimSpace(values.ExpectExternalReceipt), "true") {
		return p005Environment{}, fmt.Errorf("HAOWORK_P005_EXPECT_EXTERNAL_RECEIPT=true is required to wait for the real Delivery Leader MCP call")
	}
	if err := corebridge.ValidateRunID(values.RunID); err != nil || strings.EqualFold(values.RunID, "RUN-P005-REAL") {
		return p005Environment{}, fmt.Errorf("HAOWORK_P005_RUN_ID must be a unique, non-default validation nonce")
	}
	return values, nil
}

func p005Mission(environmentID string) model.MissionEnvelope {
	mission, err := model.CanonicalizeMissionEnvelope(model.MissionEnvelope{ID: "MSN-P005-REAL", ProjectID: "PRJ-WORKBENCH", GoalVersion: 1, ContextID: "CTX-P005", ContextHash: "context-p005", EnvironmentID: environmentID, GovernanceTaskIDs: []string{"TASK-P005"}, CompletionCriteria: []string{"independent verify evidence"}, AllowedScopes: []string{"src/**"}, AllowedSkills: []model.MissionSkillGrant{{Name: "advisory", Version: "1.0.0"}, {Name: "mirror", Version: "1.0.0"}, {Name: "patch", Version: "1.0.0"}, {Name: "audit", Version: "1.0.0"}, {Name: "submit_execution_report", Version: "1.0.0"}}, RoleAssignments: map[model.AgentFunction]string{model.FunctionManager: "AGT-P005-MANAGER", model.FunctionDeliveryLeader: "AGT-P005-LEADER", model.FunctionResearch: "AGT-P005-RESEARCH", model.FunctionBuild: "AGT-P005-BUILD", model.FunctionVerify: "AGT-P005-VERIFY"}, RiskLevel: "L2", IssuedAt: time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC), Deadline: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		panic(err)
	}
	return mission
}

func executorStartRequest(mission model.MissionEnvelope, runID, managerPrincipalID, agentTeamsInstanceID string) executor.AgentTeamsStartRequest {
	digest := sha256.Sum256([]byte(runID))
	nonce := strings.ToUpper(hex.EncodeToString(digest[:6]))
	return executor.AgentTeamsStartRequest{RunID: runID, TaskID: "TASK-P005", StepID: "STEP-P005-" + nonce, MissionID: mission.ID, WorkItemID: "WI-P005-" + nonce, GoalVersion: mission.GoalVersion, ContextID: mission.ContextID, ContextHash: mission.ContextHash, LogicalActorID: mission.RoleAssignments[model.FunctionManager], RuntimePrincipalID: managerPrincipalID, RuntimeBindingRevision: 1, AgentFunction: model.FunctionManager, EnvironmentID: mission.EnvironmentID, AgentTeamsInstanceID: agentTeamsInstanceID}
}

func TestP005EnvironmentRejectsReservedDefaultRunID(t *testing.T) {
	for name, value := range map[string]string{
		"HAOWORK_P005_CORE_BRIDGE_URL": "http://127.0.0.1:8081", "HAOWORK_P005_CORE_BRIDGE_TOKEN": "token",
		"HAOWORK_P005_ENVIRONMENT_ID": "public", "HAOWORK_P005_RUN_ID": "RUN-P005-REAL",
		"HAOWORK_P005_MANAGER_PRINCIPAL_ID": "@manager:example", "HAOWORK_P005_AGENTTEAMS_INSTANCE_ID": "haowork-msn",
		"HAOWORK_P005_EXPECT_EXTERNAL_RECEIPT": "true",
	} {
		t.Setenv(name, value)
	}
	if _, err := loadP005Environment(); err == nil {
		t.Fatal("reserved default RunID was accepted for a real validation")
	}
}

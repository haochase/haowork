package agentteamsbridge

import (
	"testing"
	"time"
)

func TestProductionMatrixPollBudgetCoversLiveAgentReply(t *testing.T) {
	if productionMatrixPollInterval != 0 {
		t.Fatalf("production Matrix polling adds a duplicate client sleep: %v", productionMatrixPollInterval)
	}
	if productionMatrixPollLimit*matrixV3SyncTimeout < 60*time.Second {
		t.Fatalf("production Matrix poll budget = %s", productionMatrixPollLimit*matrixV3SyncTimeout)
	}
}

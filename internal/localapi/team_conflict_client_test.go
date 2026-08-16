package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

func TestClientPreservesManualMergeReplacementAndConfirmation(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != teamPath+"/conflicts/CNF-1/resolve" || r.Header.Get(controlHeader) != testControlKey {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Action      string        `json:"action"`
			Replacement []model.Event `json:"replacement"`
			Confirmed   bool          `json:"confirmed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Action != team.ManualMerge || !request.Confirmed || len(request.Replacement) != 1 || request.Replacement[0].ID != "EVT-MERGE" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(team.PushResult{Status: team.PushAccepted, Materialized: true})
	}))
	defer api.Close()
	client := NewClient(localcore.Metadata{Endpoint: api.URL, ControlKey: testControlKey})
	_, err := client.ResolveTeamConflictRequest(context.Background(), "CNF-1", team.ConflictResolutionRequest{Action: team.ManualMerge, Replacement: []model.Event{{ID: "EVT-MERGE"}}, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
}

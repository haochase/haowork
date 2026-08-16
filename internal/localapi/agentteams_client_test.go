package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestAgentTeamsClientUsesSingleRequestAndStablePaths(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/missions" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(MissionResponse{Missions: []model.MissionEnvelope{}})
	}))
	defer server.Close()
	client := &Client{endpoint: server.URL, httpClient: server.Client()}
	if _, err := client.Missions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

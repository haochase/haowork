package agentteamsbridge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestUnsupportedProfileFailsWithoutCallingApply(t *testing.T) {
	applied := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			applied = true
		}
		if r.URL.Path == "/api/v1/capabilities" {
			_, _ = w.Write([]byte(`{"name":"hi-claw/agent-teams","version":"v1.2.0","apiVersion":"hiclaw.io/v1beta1","resourceKinds":{"Manager":true,"Team":true,"Worker":true,"Human":true},"controller":true,"matrix":true,"minio":true,"higressMCP":true}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := agentteamsbridge.NewControlClient(server.URL, server.Client())
	if err := client.Apply(context.Background(), agentteamsbridge.Resource{APIVersion: "hiclaw.io/v1beta1", Kind: "Manager", Name: "haowork", Spec: []byte(`{}`)}); err == nil {
		t.Fatal("Apply() succeeded with unsupported profile")
	}
	if applied {
		t.Fatal("Apply() contacted a write endpoint for unsupported profile")
	}
}

func TestControlClientNeverRedirectsOrRetriesWriteRequests(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/capabilities":
			_, _ = w.Write([]byte(`{"name":"hi-claw/agent-teams","version":"v1.1.2","apiVersion":"hiclaw.io/v1beta1","resourceKinds":{"Manager":true,"Team":true,"Worker":true,"Human":true},"controller":true,"matrix":true,"minio":true,"higressMCP":true}`))
		case "/api/v1/managers":
			posts++
			w.Header().Set("Location", "/other")
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := agentteamsbridge.NewControlClient(server.URL, server.Client())
	if err := client.Apply(context.Background(), agentteamsbridge.Resource{APIVersion: "hiclaw.io/v1beta1", Kind: "Manager", Name: "haowork", Spec: []byte(`{}`)}); err == nil {
		t.Fatal("Apply() followed redirect")
	}
	if posts != 1 {
		t.Fatalf("write attempts = %d, want 1", posts)
	}
}

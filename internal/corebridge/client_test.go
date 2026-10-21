package corebridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/corebridge"
)

func TestClientStartsRunAndReadsPersistedReceiptEvidence(t *testing.T) {
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	evidence := corebridge.RunEvidence{RunID: input.Request.RunID, SourceEventIDs: []string{testExecutionReceiptID}, ExecutionReceiptIDs: []string{testExecutionReceiptID}}
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer bridge-token" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		requests = append(requests, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/runs/start":
			var got corebridge.StartRequest
			if err := json.NewDecoder(request.Body).Decode(&got); err != nil || got.Request.RunID != input.Request.RunID || got.Mission.Hash != input.Mission.Hash {
				t.Fatalf("start request=%#v err=%v", got, err)
			}
		case request.Method == http.MethodGet && request.URL.Path == "/v1/runs/RUN-1/evidence":
		default:
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(evidence)
	}))
	defer server.Close()
	client, err := corebridge.NewClient(server.URL, "bridge-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	started, err := client.Start(context.Background(), input)
	if err != nil || started.ExecutionReceiptIDs[0] != testExecutionReceiptID {
		t.Fatalf("Start()=%#v err=%v", started, err)
	}
	persisted, err := client.Evidence(context.Background(), input.Request.RunID)
	if err != nil || persisted.ExecutionReceiptIDs[0] != testExecutionReceiptID || len(requests) != 2 {
		t.Fatalf("Evidence()=%#v requests=%#v err=%v", persisted, requests, err)
	}
}

func TestClientStartNewSendsAtomicFreshnessHeader(t *testing.T) {
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Haowork-Require-New") != "true" {
			t.Fatalf("freshness header=%q", request.Header.Get("X-Haowork-Require-New"))
		}
		_ = json.NewEncoder(response).Encode(corebridge.RunEvidence{RunID: input.Request.RunID, SourceEventIDs: []string{testExecutionReceiptID}, ExecutionReceiptIDs: []string{testExecutionReceiptID}})
	}))
	defer server.Close()
	client, err := corebridge.NewClient(server.URL, "bridge-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartNew(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func TestClientEvidenceMapsNotFoundForFreshnessPreflight(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := corebridge.NewClient(server.URL, "bridge-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Evidence(context.Background(), "RUN-NEW"); !errors.Is(err, corebridge.ErrEvidenceNotFound) {
		t.Fatalf("Evidence() error=%v", err)
	}
}

func TestClientRejectsMatrixOnlyCompletionEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(response).Encode(corebridge.RunEvidence{RunID: "RUN-1", SourceEventIDs: []string{"$matrix-event"}})
	}))
	defer server.Close()
	client, err := corebridge.NewClient(server.URL, "bridge-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	if _, err := client.Start(context.Background(), input); err == nil {
		t.Fatal("Client accepted Matrix-only completion evidence")
	}
}

func TestClientRejectsInsecureOrMalformedBaseURLBeforeRequest(t *testing.T) {
	transport := &countingRoundTripper{}
	client := &http.Client{Transport: transport}
	for _, raw := range []string{
		"http://example.com", "http://192.168.1.8:8081", "ftp://example.com", "https://user@example.com", "https://example.com/#fragment",
	} {
		if _, err := corebridge.NewClient(raw, "bridge-token", client); err == nil {
			t.Fatalf("NewClient accepted %q", raw)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("invalid BaseURL emitted %d requests", transport.calls)
	}
	for _, raw := range []string{"http://localhost:8081", "http://127.44.1.2:8081", "http://[::1]:8081", "https://bridge.example.com"} {
		if _, err := corebridge.NewClient(raw, "bridge-token", client); err != nil {
			t.Fatalf("NewClient rejected %q: %v", raw, err)
		}
	}
}

func TestClientRejectsInvalidRunIDWithoutSendingBearer(t *testing.T) {
	transport := &countingRoundTripper{}
	client, err := corebridge.NewClient("https://bridge.example.com", "bridge-token", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	input := decodeStartPayload(t, startPayload(t, "WORK-1"))
	for _, runID := range []string{" RUN-1", "RUN/1", `RUN\1`, "RUN%2F1", "RUN\n1", strings.Repeat("A", 129)} {
		before := transport.calls
		input.Request.RunID = runID
		if _, err := client.Start(context.Background(), input); err == nil {
			t.Fatalf("Start accepted RunID %q", runID)
		}
		if _, err := client.Evidence(context.Background(), runID); err == nil {
			t.Fatalf("Evidence accepted RunID %q", runID)
		}
		if transport.calls != before {
			t.Fatalf("invalid RunID %q emitted a request", runID)
		}
	}
	if transport.calls != 0 || transport.authorization != "" {
		t.Fatalf("invalid RunID emitted requests=%d authorization=%q", transport.calls, transport.authorization)
	}
}

func TestClientRejectsOversizedEvidenceAndAppliesFiniteTimeout(t *testing.T) {
	evidence := corebridge.RunEvidence{RunID: "RUN-1", SourceEventIDs: []string{testExecutionReceiptID}, ExecutionReceiptIDs: []string{testExecutionReceiptID}}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	transport := &countingRoundTripper{body: append(encoded, bytes.Repeat([]byte(" "), (1<<20)+1)...)}
	client, err := corebridge.NewClient("https://bridge.example.com", "bridge-token", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Evidence(context.Background(), "RUN-1"); err == nil {
		t.Fatal("Evidence accepted a body larger than the client limit")
	}
	if !transport.sawDeadline {
		t.Fatal("default Core Bridge client did not apply a finite timeout")
	}
}

type countingRoundTripper struct {
	calls         int
	authorization string
	body          []byte
	sawDeadline   bool
}

func (transport *countingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls++
	transport.authorization = request.Header.Get("Authorization")
	_, transport.sawDeadline = request.Context().Deadline()
	body := transport.body
	if body == nil {
		body = []byte(`{"run_id":"RUN-1"}`)
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

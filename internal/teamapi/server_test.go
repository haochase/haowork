package teamapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

func TestServerReturns401BeforeDecodingForInvalidBearer(t *testing.T) {
	server := newTestServer(t)
	for _, header := range []string{"", "Basic token", "Bearer " + testToken(3)} {
		t.Run(header, func(t *testing.T) {
			request := httptestRequest(t, http.MethodPost, "/api/v1/team/batches", `{"unknown":true}`)
			request.Header.Set("Authorization", header)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestServerMapsForbiddenCandidateAndConflictResults(t *testing.T) {
	service := &fakeTeamService{}
	server := newTestServerWithService(t, service)

	service.pushResult = team.PushResult{Status: team.PushRejected, Code: "unauthorized", Message: "owner required"}
	assertServerStatus(t, server, http.MethodPost, "/api/v1/team/batches", validBatchJSON(), http.StatusForbidden)

	service.pushResult = team.PushResult{Status: team.PushRejected, Code: "invalid_batch", Message: "invalid candidate"}
	assertServerStatus(t, server, http.MethodPost, "/api/v1/team/batches", validBatchJSON(), http.StatusUnprocessableEntity)

	service.pushResult = team.PushResult{Status: team.PushConflict, ConflictID: "CONFLICT-1", Code: team.CodeStaleBaseline, Message: "stale"}
	response := assertServerStatus(t, server, http.MethodPost, "/api/v1/team/batches", validBatchJSON(), http.StatusConflict)
	var result team.PushResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != team.PushConflict || result.Code != team.CodeStaleBaseline || result.ConflictID == "" {
		t.Fatalf("conflict response = %#v, want structured stale conflict", result)
	}
}

func TestServerReturns202ForAcceptedUnmaterializedBatch(t *testing.T) {
	service := &fakeTeamService{pushResult: team.PushResult{Status: team.PushAccepted, Materialized: false}}
	server := newTestServerWithService(t, service)
	response := assertServerStatus(t, server, http.MethodPost, "/api/v1/team/batches", validBatchJSON(), http.StatusAccepted)
	var result team.PushResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Materialized {
		t.Fatalf("Materialized = true, want false: %#v", result)
	}
}

func TestServerPreservesConflictResolutionReplacementAndConfirmation(t *testing.T) {
	service := &fakeTeamService{pushResult: team.PushResult{Status: team.PushAccepted, Materialized: true}}
	server := newTestServerWithService(t, service)
	replacement := model.Event{ID: "EVT-MERGE", Type: "requirement.planned"}
	payload, err := json.Marshal(map[string]any{"action": team.ManualMerge, "replacement": []model.Event{replacement}, "confirmed": true})
	if err != nil {
		t.Fatal(err)
	}
	response := assertServerStatus(t, server, http.MethodPost, "/api/v1/team/conflicts/CNF-1/resolve", string(payload), http.StatusOK)
	if service.resolution.ConflictID != "CNF-1" || service.resolution.Action != team.ManualMerge || !service.resolution.Confirmed || len(service.resolution.Replacement) != 1 || service.resolution.Replacement[0].ID != replacement.ID {
		t.Fatalf("resolution = %#v, want ManualMerge DTO", service.resolution)
	}
	_ = response
}

func TestServerRejectsUnknownFieldsAndMultipleJSONValues(t *testing.T) {
	server := newTestServer(t)
	for _, body := range []string{`{"batch_id":"BATCH-1","base_team_seq":1,"events":[],"extra":true}`, validBatchJSON() + validBatchJSON()} {
		response := assertServerStatus(t, server, http.MethodPost, "/api/v1/team/batches", body, http.StatusBadRequest)
		if !strings.Contains(response.Body.String(), "invalid_request") {
			t.Fatalf("response = %s, want invalid request", response.Body.String())
		}
	}
}

func TestServeRejectsNonLoopbackWithoutTLS(t *testing.T) {
	server := newTestServer(t)
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := server.Serve(listener, "", ""); err == nil {
		t.Fatal("Serve accepted a non-loopback plaintext listener")
	}
}

func TestServeAllowsLoopbackHTTPForDevelopment(t *testing.T) {
	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener, "", "") }()
	response, err := http.Get("http://" + listener.Addr().String() + "/_haowork/team/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	_ = listener.Close()
	if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Serve() = %v", err)
	}
}

func TestServeTLSUsesProvidedCertificate(t *testing.T) {
	cert, key := writeTestCertificate(t)
	server := newTestServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener, cert, key) }()
	certificateData, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificateData) {
		t.Fatal("test certificate could not be added to root pool")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	response, err := client.Get("https://" + listener.Addr().String() + "/_haowork/team/health")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.TLS == nil {
		t.Fatal("response did not use TLS")
	}
	_ = listener.Close()
	if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Serve() = %v", err)
	}
}

func newTestServer(t *testing.T) *Server { return newTestServerWithService(t, &fakeTeamService{}) }

func newTestServerWithService(t *testing.T, service TeamService) *Server {
	t.Helper()
	return &Server{ProjectID: "PRJ-TEST", ProtocolVersion: "0.1.0", Service: service, Authenticator: newTestAuthenticator(t, testToken(1), testPrincipal())}
}

func assertServerStatus(t *testing.T, server *Server, method, path, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	request := httptestRequest(t, method, path, body)
	request.Header.Set("Authorization", "Bearer "+testToken(1))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s status = %d, want %d: %s", method, path, response.Code, want, response.Body.String())
	}
	return response
}

func httptestRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var reader *strings.Reader
	switch value := body.(type) {
	case nil:
		reader = strings.NewReader("")
	case string:
		reader = strings.NewReader(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(data))
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func validBatchJSON() string { return `{"batch_id":"BATCH-1","base_team_seq":1,"events":[]}` }

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificatePath, keyPath := filepath.Join(t.TempDir(), "server.crt"), filepath.Join(t.TempDir(), "server.key")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}

type fakeTeamService struct {
	pushResult team.PushResult
	resolution team.ConflictResolutionRequest
}

func (f *fakeTeamService) Pull(context.Context, uint64) ([]model.Event, error) { return nil, nil }
func (f *fakeTeamService) Push(context.Context, team.Principal, team.PushBatch) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) Status(_ context.Context, principal team.Principal) (team.Status, error) {
	return team.Status{ProjectID: "PRJ-TEST", Principal: principal}, nil
}
func (f *fakeTeamService) ProposeGoalChange(context.Context, team.Principal, model.GoalChange, ...string) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) ApproveGoalChange(context.Context, team.Principal, string) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) RejectGoalChange(context.Context, team.Principal, string, string) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) IssueLease(context.Context, team.Principal, model.Lease) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) RenewLease(context.Context, team.Principal, string, time.Time) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) ReleaseLease(context.Context, team.Principal, string) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) RevokeLease(context.Context, team.Principal, string) (team.PushResult, error) {
	return f.pushResult, nil
}
func (f *fakeTeamService) ResolveConflict(_ context.Context, _ team.Principal, request team.ConflictResolutionRequest) (team.PushResult, error) {
	f.resolution = request
	return f.pushResult, nil
}

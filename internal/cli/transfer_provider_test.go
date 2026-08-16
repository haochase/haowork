package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
	"github.com/haochase/haowork/internal/transfer"
)

type injectedTransferProvider struct {
	config *core.TransferConfig
}

func (provider injectedTransferProvider) Load(context.Context, string) (*core.TransferConfig, error) {
	return provider.config, nil
}

func TestOpenProjectWithTransferProviderMakesTransferFacadeAvailable(t *testing.T) {
	root := initializeTransferProviderProject(t)
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	config := &core.TransferConfig{
		TargetEnvironmentID: "internal",
		PublicKeys:          map[string]ed25519.PublicKey{"return-key": private.Public().(ed25519.PublicKey)},
		ReturnSigner:        transfer.NewEd25519Signer("return-key", private),
		RuntimeBindingResolver: transfer.RuntimeBindingResolverFunc(func(context.Context, string, string) (model.RuntimeBinding, error) {
			return model.RuntimeBinding{}, nil
		}),
		ProvenanceVerifiers: transfer.ProvenanceVerifierSet{"governance-ledger": transfer.ProvenanceVerifierFunc(func(context.Context, transfer.Entry) error { return nil })},
		NewEventID:          func() string { return "EVT-TRANSFER" },
	}
	project, err := openProjectWithDependencies(context.Background(), root, &Dependencies{TransferProvider: injectedTransferProvider{config: config}})
	if err != nil {
		t.Fatal(err)
	}
	if project.Transfer == nil {
		t.Fatal("project.Transfer = nil, want provider-injected transfer service")
	}
	if project.Transfer.ReturnSigner == nil {
		t.Fatal("project.Transfer.ReturnSigner = nil, want Core-owned signer")
	}
	server := &localapi.Server{Project: project, ControlKey: "control-key"}
	defer server.Close()
	handler := server.Handler()
	cookie := authenticateLocalAPI(t, handler, "control-key")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/preview", bytes.NewBufferString(`{"archive":""}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code == http.StatusServiceUnavailable {
		t.Fatalf("injected transfer preview status = %d; provider capability was not wired into route", response.Code)
	}
}

func TestDefaultTransferProviderFailsClosedWithoutLocalConfiguration(t *testing.T) {
	root := initializeTransferProviderProject(t)
	project, err := openProjectWithDependencies(context.Background(), root, &Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if project.Transfer != nil {
		t.Fatal("default provider created a transfer service without local configuration")
	}

	server := &localapi.Server{Project: project, ControlKey: "control-key"}
	defer server.Close()
	handler := server.Handler()
	cookie := authenticateLocalAPI(t, handler, "control-key")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/transfers/preview", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("transfer preview status = %d, want %d; body=%q", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "transfer_unavailable" {
		t.Fatalf("transfer preview code = %#v, want transfer_unavailable", payload["code"])
	}
}

func initializeTransferProviderProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	_, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "transfer-provider", ProjectID: "PRJ-TRANSFER", Goal: "deliver safely", CompletionCriteria: []string{"signed archive"},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func authenticateLocalAPI(t *testing.T, handler http.Handler, controlKey string) *http.Cookie {
	t.Helper()
	bootstrapRequest := httptest.NewRequest(http.MethodPost, "/_haowork/browser-sessions", nil)
	bootstrapRequest.Header.Set("X-Haowork-Control", controlKey)
	bootstrapResponse := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusCreated {
		t.Fatalf("browser session status = %d; body=%q", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	var token struct {
		BootstrapToken string `json:"bootstrap_token"`
	}
	if err := json.Unmarshal(bootstrapResponse.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	sessionRequest := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	sessionRequest.Header.Set("X-Haowork-Bootstrap", token.BootstrapToken)
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusNoContent {
		t.Fatalf("session status = %d; body=%q", sessionResponse.Code, sessionResponse.Body.String())
	}
	cookies := sessionResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v, want one", cookies)
	}
	return cookies[0]
}

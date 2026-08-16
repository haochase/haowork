package teamapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestStaticAuthenticatorRejectsMissingMalformedAndUnknownBearerTokens(t *testing.T) {
	auth := newTestAuthenticator(t, testToken(1), testPrincipal())
	for _, header := range []string{"", "Basic token", "Bearer", "Bearer " + testToken(3)} {
		t.Run(header, func(t *testing.T) {
			request := httptestRequest(t, http.MethodGet, "/api/v1/team/status", nil)
			request.Header.Set("Authorization", header)
			if _, err := auth.Authenticate(context.Background(), request); err == nil {
				t.Fatalf("Authenticate(%q) succeeded, want rejection", header)
			}
		})
	}
}

func TestStaticAuthenticatorLoadsDigestOnlyStableIdentity(t *testing.T) {
	token := testToken(2)
	principal := testPrincipal()
	auth := newTestAuthenticator(t, token, principal)
	request := httptestRequest(t, http.MethodGet, "/api/v1/team/status", nil)
	request.Header.Set("Authorization", "Bearer "+token)

	got, err := auth.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, principal) {
		t.Fatalf("principal = %#v, want %#v", got, principal)
	}
}

func TestTokenDigestRequiresAtLeastThirtyTwoRandomBytes(t *testing.T) {
	short := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31))
	if _, err := TokenSHA256(short); err == nil {
		t.Fatal("TokenSHA256 accepted a token shorter than 32 bytes")
	}
}

func newTestAuthenticator(t *testing.T, token string, principal Principal) *StaticAuthenticator {
	t.Helper()
	digest, err := TokenSHA256(token)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "team-auth.json")
	config := StaticAuthFile{Credentials: []StaticCredential{{TokenSHA256: digest, Principal: principal}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := LoadStaticAuthenticator(path)
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func testToken(seed byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32))
}

func testPrincipal() Principal {
	return Principal{
		AuthenticatedPrincipal: "subject-owner",
		Actor:                  model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
		DeviceID:               "device-owner", EnvironmentID: "development", FunctionalIdentity: "owner",
		AllowedSkills: []string{"review"},
	}
}

package teamapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/haochase/haowork/internal/team"
)

var ErrUnauthenticated = errors.New("team request is unauthenticated")

// Principal is the stable authenticated identity carried to the Team service.
type Principal = team.Principal

type Authenticator interface {
	Authenticate(context.Context, *http.Request) (team.Principal, error)
}

type StaticCredential struct {
	TokenSHA256 string         `json:"token_sha256"`
	Principal   team.Principal `json:"principal"`
}

// StaticAuthFile is supplied explicitly outside a Capsule. It intentionally
// contains digests only, so authenticators never serialize bearer tokens.
type StaticAuthFile struct {
	Credentials []StaticCredential `json:"credentials"`
}

type staticCredential struct {
	digest    [sha256.Size]byte
	principal team.Principal
}

type StaticAuthenticator struct {
	credentials []staticCredential
}

func LoadStaticAuthenticator(path string) (*StaticAuthenticator, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config StaticAuthFile
	if err := decodeStrictJSON(file, &config); err != nil {
		return nil, fmt.Errorf("decode static team auth file: %w", err)
	}
	if len(config.Credentials) == 0 {
		return nil, errors.New("static team auth file has no credentials")
	}
	authenticator := &StaticAuthenticator{credentials: make([]staticCredential, 0, len(config.Credentials))}
	seen := make(map[[sha256.Size]byte]struct{}, len(config.Credentials))
	for _, credential := range config.Credentials {
		digest, err := parseDigest(credential.TokenSHA256)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[digest]; exists {
			return nil, errors.New("static team auth file has duplicate token digest")
		}
		seen[digest] = struct{}{}
		if err := validatePrincipal(credential.Principal); err != nil {
			return nil, err
		}
		authenticator.credentials = append(authenticator.credentials, staticCredential{digest: digest, principal: credential.Principal})
	}
	return authenticator, nil
}

func (authenticator *StaticAuthenticator) Authenticate(_ context.Context, request *http.Request) (team.Principal, error) {
	if authenticator == nil {
		return team.Principal{}, ErrUnauthenticated
	}
	token, err := bearerToken(request.Header.Get("Authorization"))
	if err != nil {
		return team.Principal{}, ErrUnauthenticated
	}
	digest, err := tokenDigest(token)
	if err != nil {
		return team.Principal{}, ErrUnauthenticated
	}
	for _, credential := range authenticator.credentials {
		if subtle.ConstantTimeCompare(digest[:], credential.digest[:]) == 1 {
			return credential.principal, nil
		}
	}
	return team.Principal{}, ErrUnauthenticated
}

// TokenSHA256 validates an opaque base64url token representing at least 32
// random bytes, then returns the persisted digest format.
func TokenSHA256(token string) (string, error) {
	digest, err := tokenDigest(token)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

func bearerToken(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", ErrUnauthenticated
	}
	return parts[1], nil
}

func tokenDigest(token string) ([sha256.Size]byte, error) {
	var empty [sha256.Size]byte
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) < 32 {
		return empty, errors.New("team bearer token must contain at least 32 random bytes")
	}
	return sha256.Sum256(raw), nil
}

func parseDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return digest, errors.New("token_sha256 must be a SHA-256 hex digest")
	}
	copy(digest[:], decoded)
	return digest, nil
}

func validatePrincipal(principal team.Principal) error {
	if strings.TrimSpace(principal.AuthenticatedPrincipal) == "" || strings.TrimSpace(principal.Actor.ID) == "" || principal.Actor.Kind == "" || principal.Actor.Role == "" || strings.TrimSpace(principal.DeviceID) == "" || strings.TrimSpace(principal.EnvironmentID) == "" {
		return errors.New("static team credential has incomplete stable identity")
	}
	return nil
}

func decodeStrictJSON(reader io.Reader, output any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return nil
}

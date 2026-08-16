package skillapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultHostReadHeaderTimeout = 5 * time.Second

// RuntimeConsumerBinding is the secret-derived association between one
// Higress/AgentTeams consumer credential digest and an already registered
// Haowork runtime principal. It intentionally stores no plaintext credential.
type RuntimeConsumerBinding struct {
	ConsumerName     string
	CredentialSHA256 string
	Principal        RuntimePrincipal
}

// RuntimeConsumerAuthenticator accepts a bearer credential only when its
// SHA-256 digest matches one mounted, server-owned consumer binding. MCP
// request arguments and arbitrary request headers never contribute identity.
type RuntimeConsumerAuthenticator struct {
	bindings []runtimeConsumerBinding
}

type runtimeConsumerBinding struct {
	credentialDigest []byte
	principal        RuntimePrincipal
}

// NewRuntimeConsumerAuthenticator validates the server-owned consumer map.
// It keeps only credential digests, never the raw Higress gateway key.
func NewRuntimeConsumerAuthenticator(bindings []RuntimeConsumerBinding) (*RuntimeConsumerAuthenticator, error) {
	if len(bindings) == 0 {
		return nil, errors.New("at least one runtime consumer binding is required")
	}
	result := &RuntimeConsumerAuthenticator{bindings: make([]runtimeConsumerBinding, 0, len(bindings))}
	seenDigests := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.ConsumerName) == "" {
			return nil, errors.New("runtime consumer name is required")
		}
		digest, err := decodeCredentialDigest(binding.CredentialSHA256)
		if err != nil {
			return nil, err
		}
		key := string(digest)
		if _, exists := seenDigests[key]; exists {
			return nil, errors.New("runtime consumer credentials must be unique")
		}
		seenDigests[key] = struct{}{}
		principal, err := validRuntimePrincipal(binding.Principal)
		if err != nil {
			return nil, err
		}
		result.bindings = append(result.bindings, runtimeConsumerBinding{credentialDigest: digest, principal: principal})
	}
	return result, nil
}

// Authenticate derives the complete runtime identity from the consumer
// credential. There is intentionally no fallback to query, cookie, or actor
// parameters supplied by an MCP caller.
func (authenticator *RuntimeConsumerAuthenticator) Authenticate(_ context.Context, request *http.Request) (RuntimePrincipal, error) {
	if authenticator == nil || len(authenticator.bindings) == 0 || request == nil {
		return RuntimePrincipal{}, ErrUnauthenticated
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return RuntimePrincipal{}, ErrUnauthenticated
	}
	credential, ok := bearerCredential(values[0])
	if !ok {
		return RuntimePrincipal{}, ErrUnauthenticated
	}
	computed := sha256.Sum256([]byte(credential))
	var principal RuntimePrincipal
	matches := 0
	for _, binding := range authenticator.bindings {
		if subtle.ConstantTimeCompare(binding.credentialDigest, computed[:]) == 1 {
			principal = binding.principal
			matches++
		}
	}
	if matches != 1 {
		return RuntimePrincipal{}, ErrUnauthenticated
	}
	return principal, nil
}

// ParseRuntimeConsumerAuthenticator reads the Secret document mounted for the
// MCP host. The schema is deliberately strict so an accidental plaintext key
// field cannot silently become part of the deployment contract.
func ParseRuntimeConsumerAuthenticator(input io.Reader) (*RuntimeConsumerAuthenticator, error) {
	if input == nil {
		return nil, errors.New("runtime consumer binding document is required")
	}
	var document runtimeConsumerBindingDocument
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invalid runtime consumer binding document")
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("runtime consumer binding document must contain one JSON value")
	}
	bindings := make([]RuntimeConsumerBinding, 0, len(document.Bindings))
	for _, binding := range document.Bindings {
		bindings = append(bindings, RuntimeConsumerBinding{
			ConsumerName:     binding.ConsumerName,
			CredentialSHA256: binding.CredentialSHA256,
			Principal: RuntimePrincipal{
				LogicalActorID:       binding.Principal.LogicalActorID,
				RuntimePrincipalID:   binding.Principal.RuntimePrincipalID,
				EnvironmentID:        binding.Principal.EnvironmentID,
				AgentTeamsInstanceID: binding.Principal.AgentTeamsInstanceID,
				BindingRevision:      binding.Principal.BindingRevision,
			},
		})
	}
	return NewRuntimeConsumerAuthenticator(bindings)
}

// LoadRuntimeConsumerAuthenticator opens a mounted Secret document without
// including its path contents or credentials in an error message.
func LoadRuntimeConsumerAuthenticator(path string) (*RuntimeConsumerAuthenticator, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, errors.New("runtime consumer binding document is unavailable")
	}
	defer file.Close()
	return ParseRuntimeConsumerAuthenticator(file)
}

type runtimeConsumerBindingDocument struct {
	Bindings []struct {
		ConsumerName     string                   `json:"consumer_name"`
		CredentialSHA256 string                   `json:"credential_sha256"`
		Principal        runtimePrincipalDocument `json:"principal"`
	} `json:"bindings"`
}

type runtimePrincipalDocument struct {
	LogicalActorID       string `json:"logical_actor_id"`
	RuntimePrincipalID   string `json:"runtime_principal_id"`
	EnvironmentID        string `json:"environment_id"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	BindingRevision      int    `json:"binding_revision"`
}

func decodeCredentialDigest(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || len(value) != sha256.Size*2 {
		return nil, errors.New("runtime consumer credential digest must be a SHA-256 hex string")
	}
	return decoded, nil
}

func validRuntimePrincipal(value RuntimePrincipal) (RuntimePrincipal, error) {
	value.LogicalActorID = strings.TrimSpace(value.LogicalActorID)
	value.RuntimePrincipalID = strings.TrimSpace(value.RuntimePrincipalID)
	value.EnvironmentID = strings.TrimSpace(value.EnvironmentID)
	value.AgentTeamsInstanceID = strings.TrimSpace(value.AgentTeamsInstanceID)
	if value.LogicalActorID == "" || value.RuntimePrincipalID == "" || value.EnvironmentID == "" || value.AgentTeamsInstanceID == "" || value.BindingRevision <= 0 {
		return RuntimePrincipal{}, errors.New("runtime consumer principal binding is incomplete")
	}
	return value, nil
}

func bearerCredential(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// Host is the process-facing wrapper around the governed MCP Server. It only
// serves the explicit /mcp path and does not expose Haowork's Local API.
type Host struct {
	listenAddress string
	routePath     string
	server        *Server
}

type HostConfig struct {
	ListenAddress     string
	RoutePath         string
	Server            *Server
	ReadHeaderTimeout time.Duration
}

func NewHost(config HostConfig) (*Host, error) {
	address, err := explicitListenAddress(config.ListenAddress)
	if err != nil {
		return nil, err
	}
	if config.Server == nil || config.Server.Authenticator == nil {
		return nil, errors.New("governed MCP server and authenticator are required")
	}
	routePath, err := governedMCPRoutePath(config.RoutePath)
	if err != nil {
		return nil, err
	}
	return &Host{listenAddress: address, routePath: routePath, server: config.Server}, nil
}

func (host *Host) Handler() http.Handler {
	if host == nil || host.server == nil {
		return http.NotFoundHandler()
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", host.server.Handler())
	if host.routePath != "/mcp" {
		mux.Handle(host.routePath, host.server.Handler())
	}
	return mux
}

func governedMCPRoutePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/mcp" {
		return "/mcp", nil
	}
	if !strings.HasPrefix(value, "/mcp-servers/") || strings.ContainsAny(value, "?#\\") {
		return "", errors.New("MCP host route path is invalid")
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(parts) != 3 || parts[0] != "mcp-servers" || parts[2] != "mcp" || !dnsLabel(parts[1]) {
		return "", errors.New("MCP host route path is invalid")
	}
	return value, nil
}

func dnsLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func (host *Host) ListenAddress() string {
	if host == nil {
		return ""
	}
	return host.listenAddress
}

// Serve runs the governed MCP endpoint until the supplied context is
// cancelled. It does not install a browser-facing or Local API endpoint.
func (host *Host) Serve(ctx context.Context) error {
	if host == nil || host.server == nil {
		return errors.New("MCP host is required")
	}
	listener, err := net.Listen("tcp", host.listenAddress)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              host.listenAddress,
		Handler:           host.Handler(),
		ReadHeaderTimeout: defaultHostReadHeaderTimeout,
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-stopped:
		}
	}()
	err = server.Serve(listener)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func explicitListenAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return "", errors.New("MCP host requires an explicit host and port")
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return "", errors.New("MCP host port is invalid")
	}
	if net.ParseIP(host) == nil && !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("MCP host address must use an IP address or localhost")
	}
	return value, nil
}

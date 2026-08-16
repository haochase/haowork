package agentteamsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// HigressConfig configures a read-only inspection client for the official
// Higress Console API. Credentials are sent only to /session/login and are
// never part of returned values or error text.
type HigressConfig struct {
	ConsoleURL                string
	Username                  string
	Password                  string
	AllowInsecureClusterLocal bool
	Client                    *http.Client
	MaxBodyBytes              int64
}

type HigressExpectation struct {
	ConsumerName  string
	RouteName     string
	MCPServerName string
}

// HigressInspection is browser/trace safe: it contains names and validation
// facts only, never session cookies, credential values, or raw Console JSON.
type HigressInspection struct {
	ConsumerName  string `json:"consumer_name"`
	RouteName     string `json:"route_name"`
	MCPServerName string `json:"mcp_server_name"`
}

type HigressInspector struct {
	base         *url.URL
	client       *http.Client
	username     string
	password     string
	maxBodyBytes int64
}

const defaultHigressMaxBodyBytes int64 = 1 << 20

func NewHigressInspector(config HigressConfig) (*HigressInspector, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.ConsoleURL), "/"))
	clusterLocalHTTP := base != nil && base.Scheme == "http" && config.AllowInsecureClusterLocal && isClusterLocalHost(base.Hostname())
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || (base.Scheme != "https" && !isLoopbackHost(base.Host) && !clusterLocalHTTP) {
		return nil, ErrInsecureControlPlane
	}
	if strings.TrimSpace(config.Username) == "" || config.Password == "" {
		return nil, errors.New("Higress console credentials are required")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := config.Client
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.Jar = jar
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultHigressMaxBodyBytes
	}
	return &HigressInspector{base: base, client: &copy, username: strings.TrimSpace(config.Username), password: config.Password, maxBodyBytes: maxBodyBytes}, nil
}

// Inspect verifies the consumer, its gateway route authorization, and the
// specific MCP binding using only GET inspection after an authenticated login.
func (inspector *HigressInspector) Inspect(ctx context.Context, expectation HigressExpectation) (HigressInspection, error) {
	if inspector == nil || inspector.base == nil || inspector.client == nil {
		return HigressInspection{}, errors.New("Higress inspector is required")
	}
	expectation.ConsumerName = strings.TrimSpace(expectation.ConsumerName)
	expectation.RouteName = strings.TrimSpace(expectation.RouteName)
	expectation.MCPServerName = strings.TrimSpace(expectation.MCPServerName)
	if expectation.ConsumerName == "" || expectation.RouteName == "" || expectation.MCPServerName == "" {
		return HigressInspection{}, errors.New("Higress consumer, route, and MCP server are required")
	}
	if err := inspector.login(ctx); err != nil {
		return HigressInspection{}, err
	}
	consumers, err := inspector.listNamed(ctx, "/v1/consumers")
	if err != nil {
		return HigressInspection{}, err
	}
	if !containsHigressName(consumers, expectation.ConsumerName) {
		return HigressInspection{}, errors.New("expected Higress consumer is not registered")
	}
	routes, err := inspector.listRoutes(ctx)
	if err != nil {
		return HigressInspection{}, err
	}
	route, exists := routes[expectation.RouteName]
	if !exists || !containsHigressName(route.AuthConfig.AllowedConsumers, expectation.ConsumerName) {
		return HigressInspection{}, errors.New("expected Higress route authorization is not registered")
	}
	mcpServers, err := inspector.listNamed(ctx, "/v1/mcpServer")
	if err != nil {
		return HigressInspection{}, err
	}
	if !containsHigressName(mcpServers, expectation.MCPServerName) {
		return HigressInspection{}, errors.New("expected Higress MCP server is not registered")
	}
	bindings, err := inspector.listMCPBindings(ctx, expectation.MCPServerName, expectation.RouteName, expectation.ConsumerName)
	if err != nil {
		return HigressInspection{}, err
	}
	if !bindings {
		return HigressInspection{}, errors.New("expected Higress MCP binding is not registered")
	}
	return HigressInspection{ConsumerName: expectation.ConsumerName, RouteName: expectation.RouteName, MCPServerName: expectation.MCPServerName}, nil
}

func (inspector *HigressInspector) login(ctx context.Context) error {
	var response higressResponseMeta
	if err := inspector.doJSON(ctx, http.MethodPost, "/session/login", map[string]string{"username": inspector.username, "password": inspector.password}, &response); err != nil {
		return err
	}
	return response.validate()
}

func (inspector *HigressInspector) listNamed(ctx context.Context, endpoint string) ([]string, error) {
	var response higressNamedList
	if err := inspector.doJSON(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if err := response.validate(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(response.Data))
	for _, value := range response.Data {
		if name := strings.TrimSpace(value.Name); name != "" {
			result = append(result, name)
		}
	}
	return result, nil
}

func (inspector *HigressInspector) listRoutes(ctx context.Context) (map[string]higressRoute, error) {
	var response higressRouteList
	if err := inspector.doJSON(ctx, http.MethodGet, "/v1/routes", nil, &response); err != nil {
		return nil, err
	}
	if err := response.validate(); err != nil {
		return nil, err
	}
	routes := make(map[string]higressRoute, len(response.Data))
	for _, route := range response.Data {
		if name := strings.TrimSpace(route.Name); name != "" {
			routes[name] = route
		}
	}
	return routes, nil
}

func (inspector *HigressInspector) listMCPBindings(ctx context.Context, mcpServerName, routeName, consumerName string) (bool, error) {
	query := url.Values{"mcpServerName": []string{mcpServerName}, "consumerName": []string{consumerName}}
	var response higressMCPBindingList
	if err := inspector.doJSON(ctx, http.MethodGet, "/v1/mcpServer/consumers?"+query.Encode(), nil, &response); err != nil {
		return false, err
	}
	if err := response.validate(); err != nil {
		return false, err
	}
	for _, binding := range response.Data {
		// The official Console query is keyed by the logical MCP server name,
		// while returned authorization tuples use its generated route name.
		if strings.TrimSpace(binding.MCPServerName) == routeName && strings.TrimSpace(binding.ConsumerName) == consumerName {
			return true, nil
		}
	}
	// A count alone cannot prove that this consumer is bound to this MCP
	// server. Require the explicit tuple returned by the Console API.
	return false, nil
}

func (inspector *HigressInspector) doJSON(ctx context.Context, method, path string, input, output any) error {
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || !strings.HasPrefix(relative.Path, "/") {
		return errors.New("invalid Higress Console endpoint")
	}
	endpoint := inspector.base.ResolveReference(relative)
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := inspector.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Higress Console %s %s returned HTTP %d", method, relative.Path, response.StatusCode)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, inspector.maxBodyBytes+1))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("Higress Console response contains trailing JSON")
		}
		return err
	}
	return nil
}

// higressResponseMeta models the official Console response wrapper. Values
// outside this small envelope are intentionally ignored and never become
// authorization input.
type higressResponseMeta struct {
	Success  *bool  `json:"success,omitempty"`
	Code     *int   `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Total    *int   `json:"total,omitempty"`
	PageNum  *int   `json:"pageNum,omitempty"`
	PageSize *int   `json:"pageSize,omitempty"`
}

func (response higressResponseMeta) validate() error {
	if response.Success != nil && !*response.Success {
		return errors.New("Higress Console returned an unsuccessful response")
	}
	// The cached official paginated responses omit code. When a deployment
	// adds this common wrapper, only its conventional success values are safe
	// to combine with data; all other HTTP-200 business outcomes fail closed.
	if response.Code != nil && *response.Code != 0 && *response.Code != http.StatusOK {
		return errors.New("Higress Console returned a non-success business code")
	}
	for _, value := range []*int{response.Total, response.PageNum, response.PageSize} {
		if value != nil && *value < 0 {
			return errors.New("Higress Console returned a negative pagination value")
		}
	}
	return nil
}

type higressNamedList struct {
	higressResponseMeta
	Data []struct {
		Name string `json:"name"`
	} `json:"data"`
}

type higressRouteList struct {
	higressResponseMeta
	Data []higressRoute `json:"data"`
}

type higressRoute struct {
	Name       string `json:"name"`
	AuthConfig struct {
		AllowedConsumers []string `json:"allowedConsumers"`
	} `json:"authConfig"`
}

type higressMCPBindingList struct {
	higressResponseMeta
	Data []struct {
		MCPServerName string `json:"mcpServerName"`
		ConsumerName  string `json:"consumerName"`
	} `json:"data"`
}

func containsHigressName(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}

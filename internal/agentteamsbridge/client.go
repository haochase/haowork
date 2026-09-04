package agentteamsbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

type ResourceStatus struct {
	Name                string            `json:"name"`
	Phase               string            `json:"phase"`
	RuntimePrincipalIDs map[string]string `json:"runtimePrincipalIDs"`
	LeaderRoomID        string            `json:"leaderRoomID"`
	TeamRoomID          string            `json:"teamRoomID"`
}

type RuntimeTopology struct {
	MissionID, TeamName, ManagerPrincipalID, LeaderPrincipalID                     string
	WorkerPrincipalIDs                                                             map[model.AgentFunction]string
	HumanName, HumanUID, HumanPrincipalID, ManagerRoomID, LeaderRoomID, TeamRoomID string
}

type ControlPlane interface {
	Detect(context.Context) (CapabilityProfile, error)
	Apply(context.Context, Resource) error
	GetTeam(context.Context, string) (ResourceStatus, error)
	StopTeam(context.Context, string) (ResourceStatus, error)
}

type ControlClient struct {
	base             *url.URL
	client           *http.Client
	expectedIdentity string
}

// These identifiers are confined to the migration-only ControlClient. The
// official production constructor never instantiates this client.
const (
	legacyControlName       = "hi-claw/agent-teams"
	legacyControlVersion    = "v1.1.2"
	legacyControlAPIVersion = "hiclaw.io/v1beta1"
)

func NewControlClient(baseURL string, client *http.Client) *ControlClient {
	return NewPinnedControlClient(baseURL, client, "")
}

func NewPinnedControlClient(baseURL string, client *http.Client, expectedIdentity string) *ControlClient {
	base, _ := url.Parse(strings.TrimRight(baseURL, "/"))
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ControlClient{base: base, client: &copy, expectedIdentity: strings.TrimSpace(expectedIdentity)}
}

func (client *ControlClient) Detect(ctx context.Context) (CapabilityProfile, error) {
	var profile CapabilityProfile
	if err := client.do(ctx, http.MethodGet, "/api/v1/capabilities", nil, &profile); err != nil {
		return CapabilityProfile{}, err
	}
	if client.expectedIdentity != "" && profile.Identity != client.expectedIdentity {
		return CapabilityProfile{}, fmt.Errorf("%w: got %q", ErrIdentityMismatch, profile.Identity)
	}
	return profile, nil
}

func (client *ControlClient) Apply(ctx context.Context, resource Resource) error {
	profile, err := client.Detect(ctx)
	if err != nil {
		return err
	}
	if !isLegacyControlProfile(profile) {
		return fmt.Errorf("%w: got %s %s", ErrUnsupportedProfile, profile.Name, profile.Version)
	}
	path, ok := resourcePath(resource.Kind)
	if !ok || resource.APIVersion != legacyControlAPIVersion {
		return fmt.Errorf("%w: unsupported resource %s/%s", ErrUnsupportedProfile, resource.APIVersion, resource.Kind)
	}
	body, err := resource.CanonicalJSON()
	if err != nil {
		return err
	}
	return client.do(ctx, http.MethodPost, path, body, nil)
}

func isLegacyControlProfile(profile CapabilityProfile) bool {
	if profile.Name != legacyControlName || profile.Version != legacyControlVersion || profile.APIVersion != legacyControlAPIVersion || !profile.Controller || !profile.Matrix || !profile.MinIO || !profile.HigressMCP {
		return false
	}
	for _, kind := range []string{"Manager", "Team", "Worker", "Human"} {
		if !profile.ResourceKinds[kind] {
			return false
		}
	}
	return true
}

func (client *ControlClient) GetTeam(ctx context.Context, name string) (ResourceStatus, error) {
	var status ResourceStatus
	err := client.do(ctx, http.MethodGet, "/api/v1/teams/"+url.PathEscape(name), nil, &status)
	return status, err
}

func (client *ControlClient) StopTeam(ctx context.Context, name string) (ResourceStatus, error) {
	var status ResourceStatus
	err := client.do(ctx, http.MethodDelete, "/api/v1/teams/"+url.PathEscape(name), nil, &status)
	if err != nil {
		return ResourceStatus{}, err
	}
	if !strings.EqualFold(status.Phase, "Stopped") && !strings.EqualFold(status.Phase, "Cancelled") {
		return ResourceStatus{}, fmt.Errorf("AgentTeams team %q stop was not acknowledged", name)
	}
	return status, nil
}

func (client *ControlClient) do(ctx context.Context, method, path string, body []byte, output any) error {
	if client == nil || client.base == nil {
		return fmt.Errorf("AgentTeams control endpoint is required")
	}
	if client.base.Scheme != "https" && !isLoopbackHost(client.base.Host) {
		return ErrInsecureControlPlane
	}
	endpoint := client.base.ResolveReference(&url.URL{Path: path})
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("AgentTeams %s %s returned %s", method, path, response.Status)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func resourcePath(kind string) (string, bool) {
	switch kind {
	case "Manager":
		return "/api/v1/managers", true
	case "Team":
		return "/api/v1/teams", true
	case "Worker":
		return "/api/v1/workers", true
	case "Human":
		return "/api/v1/humans", true
	default:
		return "", false
	}
}

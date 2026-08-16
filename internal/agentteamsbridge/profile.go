// Package agentteamsbridge binds Haowork's governed execution boundary to AgentTeams.
package agentteamsbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

const (
	StableName       = "agentscope-ai/AgentTeams"
	StableVersion    = "v1.2.2"
	StableAPIVersion = "agentteams.io/v1beta1"
)

var (
	ErrUnsupportedProfile   = errors.New("unsupported AgentTeams capability profile")
	ErrInsecureControlPlane = errors.New("AgentTeams control endpoint must use loopback or TLS")
	ErrIdentityMismatch     = errors.New("AgentTeams control identity mismatch")
)

type CapabilityProfile struct {
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	APIVersion    string          `json:"apiVersion"`
	ResourceKinds map[string]bool `json:"resourceKinds"`
	Controller    bool            `json:"controller"`
	Matrix        bool            `json:"matrix"`
	MinIO         bool            `json:"minio"`
	HigressMCP    bool            `json:"higressMCP"`
	Identity      string          `json:"identity,omitempty"`
}

func StableProfile() CapabilityProfile {
	return CapabilityProfile{
		Name: StableName, Version: StableVersion, APIVersion: StableAPIVersion,
		ResourceKinds: map[string]bool{"Manager": true, "Team": true, "Worker": true, "Human": true},
		Controller:    true, Matrix: true, MinIO: true, HigressMCP: true,
	}
}

func (profile CapabilityProfile) IsStable() bool {
	if profile.Name != StableName || profile.Version != StableVersion || profile.APIVersion != StableAPIVersion || !profile.Controller || !profile.Matrix || !profile.MinIO || !profile.HigressMCP {
		return false
	}
	for _, kind := range []string{"Manager", "Team", "Worker", "Human"} {
		if !profile.ResourceKinds[kind] {
			return false
		}
	}
	return true
}

type Resource struct {
	APIVersion, Kind, Name string
	Spec                   json.RawMessage
}

func (resource Resource) CanonicalJSON() ([]byte, error) {
	if strings.TrimSpace(resource.APIVersion) == "" || strings.TrimSpace(resource.Kind) == "" || strings.TrimSpace(resource.Name) == "" || !json.Valid(resource.Spec) {
		return nil, errors.New("resource api version, kind, name, and JSON spec are required")
	}
	var spec any
	if err := json.Unmarshal(resource.Spec, &spec); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec any `json:"spec"`
	}{APIVersion: resource.APIVersion, Kind: resource.Kind, Metadata: struct {
		Name string `json:"name"`
	}{Name: resource.Name}, Spec: spec})
}

func (resource Resource) SHA256() (string, error) {
	encoded, err := resource.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// YAML returns deterministic JSON, which is also a valid YAML document.
func (resource Resource) YAML() ([]byte, error) { return resource.CanonicalJSON() }

func stableToolGrants(skills []string) []string {
	grants := append([]string(nil), skills...)
	sort.Strings(grants)
	return grants
}

package agentteamsbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	HaoworkAgentTeamsFieldManager = "haowork-agentteams-bridge"
	HaoworkMissionLabel           = "haowork.io/mission"
	HaoworkEnvironmentLabel       = "haowork.io/environment"
	HaoworkAgentFunctionLabel     = "haowork.io/agent-function"
	HaoworkMissionHashAnnotation  = "haowork.io/mission-hash"
	HaoworkMissionIDAnnotation    = "haowork.io/mission-id"
)

// OfficialResourceConfig contains only the non-secret facts needed to render
// AgentTeams v1.2.2 CRDs. Higress consumer credentials stay outside CR specs.
type OfficialResourceConfig struct {
	Namespace      string
	ControllerName string
	Model          string
	ManagerRuntime string
	WorkerRuntime  string
	MCPServerName  string
	MCPServerURL   string
	MCPTransport   string
	HumanName      string
}

type OfficialManager struct{ Object *unstructured.Unstructured }
type OfficialWorker struct{ Object *unstructured.Unstructured }
type OfficialTeam struct{ Object *unstructured.Unstructured }
type OfficialHuman struct{ Object *unstructured.Unstructured }

// OfficialMissionResources is the exact CRD topology that Haowork requests
// from AgentTeams. Runtime identities are deliberately absent: they must be
// read from the controller-observed status fields after reconciliation.
type OfficialMissionResources struct {
	Manager OfficialManager
	Workers []OfficialWorker
	Team    OfficialTeam
	Human   OfficialHuman
}

func (resources OfficialMissionResources) All() []*unstructured.Unstructured {
	objects := make([]*unstructured.Unstructured, 0, len(resources.Workers)+3)
	if resources.Manager.Object != nil {
		objects = append(objects, resources.Manager.Object)
	}
	for _, worker := range resources.Workers {
		if worker.Object != nil {
			objects = append(objects, worker.Object)
		}
	}
	if resources.Human.Object != nil {
		objects = append(objects, resources.Human.Object)
	}
	if resources.Team.Object != nil {
		objects = append(objects, resources.Team.Object)
	}
	return objects
}

// RenderOfficialMissionResources produces only the public AgentTeams v1beta1
// fields documented by the pinned upstream CRDs. No runtime principal, room,
// token, or private-control-plane field is encoded in the request.
func RenderOfficialMissionResources(envelope model.MissionEnvelope, config OfficialResourceConfig) (OfficialMissionResources, error) {
	if err := validateOfficialResourceInput(envelope, config); err != nil {
		return OfficialMissionResources{}, err
	}
	teamName := officialTeamName(envelope.ID)
	labels := officialLabels(envelope, config)
	annotations := map[string]string{
		HaoworkMissionHashAnnotation: strings.TrimSpace(envelope.Hash),
		HaoworkMissionIDAnnotation:   strings.TrimSpace(envelope.ID),
	}

	manager := officialObject("Manager", teamName+"-manager", config.Namespace, labels, annotations, map[string]any{
		"model":      strings.TrimSpace(config.Model),
		"runtime":    strings.TrimSpace(config.ManagerRuntime),
		"skills":     missionSkillNames(envelope),
		"mcpServers": officialMCPServers(config),
		"state":      "Running",
	})

	functions := []model.AgentFunction{
		model.FunctionDeliveryLeader,
		model.FunctionResearch,
		model.FunctionBuild,
		model.FunctionVerify,
	}
	workers := make([]OfficialWorker, 0, len(functions))
	members := make([]any, 0, len(functions))
	for _, function := range functions {
		workerName := teamName + "-" + officialFunctionName(function)
		workerLabels := cloneStringMap(labels)
		workerLabels[HaoworkAgentFunctionLabel] = string(function)
		workerAnnotations := cloneStringMap(annotations)
		workerAnnotations["haowork.io/logical-actor-id"] = strings.TrimSpace(envelope.RoleAssignments[function])
		worker := officialObject("Worker", workerName, config.Namespace, workerLabels, workerAnnotations, map[string]any{
			"model":      strings.TrimSpace(config.Model),
			"runtime":    strings.TrimSpace(config.WorkerRuntime),
			"workerName": workerName,
			"identity":   "Haowork " + string(function),
			"skills":     missionRoleSkillNames(envelope, function),
			"mcpServers": officialMCPServers(config),
			"state":      "Running",
		})
		workers = append(workers, OfficialWorker{Object: worker})
		role := "worker"
		if function == model.FunctionDeliveryLeader {
			role = "team_leader"
		}
		members = append(members, map[string]any{"name": workerName, "role": role})
	}

	humanName := teamName + "-owner"
	human := officialObject("Human", humanName, config.Namespace, labels, annotations, map[string]any{
		"displayName":     strings.TrimSpace(config.HumanName),
		"username":        humanName,
		"permissionLevel": int64(2),
		"accessibleTeams": []any{teamName},
		"note":            "Haowork mission owner",
	})
	team := officialObject("Team", teamName, config.Namespace, labels, annotations, map[string]any{
		"description":   "Haowork governed mission " + strings.TrimSpace(envelope.ID),
		"teamName":      teamName,
		"workerMembers": members,
		"humanMembers":  []any{map[string]any{"name": humanName, "role": "coordinator"}},
		"admin":         map[string]any{"name": humanName},
		"peerMentions":  true,
	})

	return OfficialMissionResources{
		Manager: OfficialManager{Object: manager},
		Workers: workers,
		Team:    OfficialTeam{Object: team},
		Human:   OfficialHuman{Object: human},
	}, nil
}

func validateOfficialResourceInput(envelope model.MissionEnvelope, config OfficialResourceConfig) error {
	if strings.TrimSpace(envelope.ID) == "" || strings.TrimSpace(envelope.EnvironmentID) == "" || strings.TrimSpace(envelope.Hash) == "" {
		return fmt.Errorf("mission id, environment, and hash are required for official AgentTeams resources")
	}
	if len(strings.TrimSpace(envelope.Hash)) != sha256.Size*2 {
		return fmt.Errorf("mission hash must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(strings.TrimSpace(envelope.Hash)); err != nil {
		return fmt.Errorf("mission hash must be a SHA-256 hex digest: %w", err)
	}
	for _, function := range []model.AgentFunction{model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify} {
		if strings.TrimSpace(envelope.RoleAssignments[function]) == "" {
			return fmt.Errorf("mission assignment for %s is required", function)
		}
	}
	if len(envelope.AllowedSkills) == 0 {
		return fmt.Errorf("at least one mission skill is required")
	}
	if strings.TrimSpace(config.Namespace) == "" || strings.TrimSpace(config.ControllerName) == "" || strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.ManagerRuntime) == "" || strings.TrimSpace(config.WorkerRuntime) == "" || strings.TrimSpace(config.HumanName) == "" {
		return fmt.Errorf("official AgentTeams namespace, controller, model, runtimes, and human name are required")
	}
	if strings.TrimSpace(config.MCPServerName) == "" || strings.TrimSpace(config.MCPServerURL) == "" {
		return fmt.Errorf("official MCP server name and URL are required")
	}
	parsed, err := url.Parse(strings.TrimSpace(config.MCPServerURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("official MCP server URL is invalid")
	}
	if transport := strings.TrimSpace(config.MCPTransport); transport != "http" && transport != "sse" {
		return fmt.Errorf("official MCP transport must be http or sse")
	}
	return nil
}

func officialObject(kind, name, namespace string, labels, annotations map[string]string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": OfficialAPIGroup + "/" + OfficialAPIVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":        name,
			"namespace":   namespace,
			"labels":      stringMapToAny(labels),
			"annotations": stringMapToAny(annotations),
		},
		"spec": spec,
	}}
}

func officialLabels(envelope model.MissionEnvelope, config OfficialResourceConfig) map[string]string {
	return map[string]string{
		OfficialControllerOwnershipLabel: strings.TrimSpace(config.ControllerName),
		HaoworkMissionLabel:              officialLabelValue(envelope.ID),
		HaoworkEnvironmentLabel:          officialLabelValue(envelope.EnvironmentID),
	}
}

func officialMCPServers(config OfficialResourceConfig) []any {
	return []any{map[string]any{
		"name":      strings.TrimSpace(config.MCPServerName),
		"url":       strings.TrimSpace(config.MCPServerURL),
		"transport": strings.TrimSpace(config.MCPTransport),
	}}
}

func missionSkillNames(envelope model.MissionEnvelope) []any {
	values := make([]string, 0, len(envelope.AllowedSkills))
	for _, grant := range envelope.AllowedSkills {
		values = append(values, strings.TrimSpace(grant.Name))
	}
	return stringsToAny(stableUniqueStrings(values))
}

func missionRoleSkillNames(envelope model.MissionEnvelope, function model.AgentFunction) []any {
	allowed := roleSkillAllowlist(function)
	values := make([]string, 0, len(envelope.AllowedSkills))
	for _, grant := range envelope.AllowedSkills {
		name := strings.TrimSpace(grant.Name)
		if allowed[name] {
			values = append(values, name)
		}
	}
	return stringsToAny(stableUniqueStrings(values))
}

func stringsToAny(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func stableUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func officialTeamName(missionID string) string {
	value := officialLabelValue(missionID)
	// The longest child CR name ends in "-delivery-leader" (16 bytes). After
	// the "haowork-" prefix, keep the team base at 39 bytes so every Team,
	// Manager, Worker, and Human name
	// remains a DNS-1123 label of at most 63 bytes.
	const teamBaseMaxLength = 39
	if len(value) > teamBaseMaxLength {
		value = withIdentityDigest(value, strings.TrimSpace(missionID), teamBaseMaxLength)
	}
	return "haowork-" + value
}

func officialLabelValue(value string) string {
	original := strings.TrimSpace(value)
	value = strings.ToLower(original)
	var builder strings.Builder
	previousDash := false
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			previousDash = false
			continue
		}
		if character == '-' {
			builder.WriteRune(character)
			previousDash = true
			continue
		}
		if !previousDash {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "mission-" + shortIdentityDigest(original)
	}
	if len(result) > 63 || result != value {
		return withIdentityDigest(result, original, 63)
	}
	return result
}

func withIdentityDigest(value, original string, maxLength int) string {
	digest := shortIdentityDigest(original)
	prefixLength := maxLength - len(digest) - 1
	if prefixLength <= 0 {
		return digest[:maxLength]
	}
	if len(value) > prefixLength {
		value = value[:prefixLength]
	}
	value = strings.TrimRight(value, "-")
	if value == "" {
		value = "mission"
	}
	return value + "-" + digest
}

func shortIdentityDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:7]
}

func officialFunctionName(function model.AgentFunction) string {
	if function == model.FunctionDeliveryLeader {
		return "delivery-leader"
	}
	return string(function)
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func stringMapToAny(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

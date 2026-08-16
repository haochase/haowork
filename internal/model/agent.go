package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type AgentFunction string

const (
	FunctionManager        AgentFunction = "manager"
	FunctionDeliveryLeader AgentFunction = "delivery_leader"
	FunctionResearch       AgentFunction = "research"
	FunctionBuild          AgentFunction = "build"
	FunctionVerify         AgentFunction = "verify"
)

func IsValidRiskLevel(risk string) bool {
	switch risk {
	case "L0", "L1", "L2", "L3":
		return true
	default:
		return false
	}
}

// IsMissionLeaseBoundToBuildAssignment verifies that a mission lease targets its build logical agent.
func IsMissionLeaseBoundToBuildAssignment(lease Lease, buildAgentID string) bool {
	subjectKind := strings.TrimSpace(lease.SubjectKind)
	return (subjectKind == "logical_agent" || subjectKind == string(ActorAgent)) && strings.TrimSpace(lease.SubjectID) == strings.TrimSpace(buildAgentID)
}

// IsActiveMissionLeaseForBuildAssignment verifies every runtime binding required by a mission lease.
func IsActiveMissionLeaseForBuildAssignment(lease Lease, goalVersion int, contextID, environmentID, taskID, buildAgentID string) bool {
	return lease.Status == "active" && lease.GoalVersion == goalVersion && lease.ContextID == contextID && lease.EnvironmentID == environmentID && lease.TaskID == taskID && IsMissionLeaseBoundToBuildAssignment(lease, buildAgentID)
}

// MissionCapabilitiesWithinLease checks normalized mission grants against the lease capability sets.
func MissionCapabilitiesWithinLease(lease Lease, scopes []string, skills []MissionSkillGrant) bool {
	allowedScopes := normalizedMissionCapabilitySet(lease.AllowedScopes)
	for _, scope := range scopes {
		if _, exists := allowedScopes[strings.TrimSpace(scope)]; !exists {
			return false
		}
	}
	allowedSkills := normalizedMissionCapabilitySet(lease.AllowedSkills)
	for _, skill := range skills {
		if _, exists := allowedSkills[strings.TrimSpace(skill.Name)]; !exists {
			return false
		}
	}
	return true
}

type LogicalAgent struct {
	ID             string        `json:"id"`
	SubjectKind    ActorKind     `json:"subject_kind"`
	GovernanceRole ActorRole     `json:"governance_role"`
	Function       AgentFunction `json:"agent_function"`
	Status         string        `json:"status"`
}

type RuntimeBinding struct {
	LogicalActorID       string `json:"logical_actor_id"`
	Revision             int    `json:"revision"`
	EnvironmentID        string `json:"environment_id"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	RuntimePrincipalID   string `json:"runtime_principal_id"`
	HumanPrincipalID     string `json:"human_principal_id,omitempty"`
	LeaderRoomID         string `json:"leader_room_id,omitempty"`
	TeamRoomID           string `json:"team_room_id,omitempty"`
	Status               string `json:"status"`
}

type ApprovalRequest struct {
	ID, SubjectType, SubjectID, PayloadSHA256, RiskLevel string
	RequesterID, DeciderID, Status, DecisionReason       string
	RequestedAt, DecidedAt                               time.Time
}

type MissionSkillGrant struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MissionEnvelope is the persisted, immutable mission representation.
// The mission package aliases it to keep the event model free of import cycles.
type MissionEnvelope struct {
	ID, ProjectID, ContextID, ContextHash, LeaseID, PolicyVersion string
	GoalVersion                                                   int
	GovernanceTaskIDs, CompletionCriteria, AllowedScopes          []string
	AllowedSkills                                                 []MissionSkillGrant
	RoleAssignments                                               map[AgentFunction]string
	RiskLevel, EnvironmentID                                      string
	IssuedAt, Deadline                                            time.Time
	Hash                                                          string
}

// CanonicalizeMissionEnvelope normalizes the hash-bound mission representation.
func CanonicalizeMissionEnvelope(envelope MissionEnvelope) (MissionEnvelope, error) {
	envelope.ID = strings.TrimSpace(envelope.ID)
	envelope.ProjectID = strings.TrimSpace(envelope.ProjectID)
	envelope.ContextID = strings.TrimSpace(envelope.ContextID)
	envelope.ContextHash = strings.TrimSpace(envelope.ContextHash)
	envelope.LeaseID = strings.TrimSpace(envelope.LeaseID)
	envelope.PolicyVersion = strings.TrimSpace(envelope.PolicyVersion)
	envelope.GovernanceTaskIDs = canonicalStrings(envelope.GovernanceTaskIDs)
	envelope.CompletionCriteria = canonicalStrings(envelope.CompletionCriteria)
	envelope.AllowedScopes = canonicalStrings(envelope.AllowedScopes)
	envelope.AllowedSkills = canonicalSkills(envelope.AllowedSkills)
	envelope.RoleAssignments = canonicalAssignments(envelope.RoleAssignments)
	envelope.RiskLevel = strings.TrimSpace(envelope.RiskLevel)
	envelope.EnvironmentID = strings.TrimSpace(envelope.EnvironmentID)
	envelope.IssuedAt = envelope.IssuedAt.UTC()
	envelope.Deadline = envelope.Deadline.UTC()
	envelope.Hash = ""
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return MissionEnvelope{}, err
	}
	digest := sha256.Sum256(canonical)
	envelope.Hash = hex.EncodeToString(digest[:])
	return envelope, nil
}

// VerifyMissionEnvelope rejects modified or non-canonical persisted mission payloads.
func VerifyMissionEnvelope(envelope MissionEnvelope, leases ...Lease) error {
	if len(leases) > 1 {
		return errors.New("mission verification accepts at most one lease")
	}
	if strings.TrimSpace(envelope.Hash) == "" {
		return errors.New("mission hash is required")
	}
	if strings.TrimSpace(envelope.ID) == "" || strings.TrimSpace(envelope.ProjectID) == "" || strings.TrimSpace(envelope.ContextID) == "" || strings.TrimSpace(envelope.ContextHash) == "" || strings.TrimSpace(envelope.LeaseID) == "" || strings.TrimSpace(envelope.PolicyVersion) == "" || envelope.GoalVersion <= 0 {
		return errors.New("mission identity, context, lease, policy, and goal version are required")
	}
	if strings.TrimSpace(envelope.RiskLevel) == "" || strings.TrimSpace(envelope.EnvironmentID) == "" || envelope.IssuedAt.IsZero() || envelope.Deadline.IsZero() || !envelope.Deadline.After(envelope.IssuedAt) {
		return errors.New("mission risk, environment, issue time, and deadline are required")
	}
	if !IsValidRiskLevel(envelope.RiskLevel) {
		return errors.New("mission risk level is invalid")
	}
	if err := ValidateMissionEnvelopeContent(envelope); err != nil {
		return err
	}
	build, buildOK := envelope.RoleAssignments[FunctionBuild]
	verify, verifyOK := envelope.RoleAssignments[FunctionVerify]
	if !buildOK || !verifyOK || strings.TrimSpace(build) == "" || strings.TrimSpace(verify) == "" || build == verify {
		return errors.New("mission build and verify assignments must be distinct")
	}
	canonical, err := CanonicalizeMissionEnvelope(envelope)
	if err != nil {
		return err
	}
	if envelope.Hash != canonical.Hash {
		return errors.New("mission hash does not match canonical envelope")
	}
	persisted, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	expected, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	if !bytes.Equal(persisted, expected) {
		return errors.New("mission envelope is not canonical")
	}
	if len(leases) == 1 && !MissionCapabilitiesWithinLease(leases[0], envelope.AllowedScopes, envelope.AllowedSkills) {
		return errors.New("mission capabilities exceed the lease")
	}
	return nil
}

// ValidateMissionEnvelopeContent rejects empty mission grants and role assignments.
func ValidateMissionEnvelopeContent(envelope MissionEnvelope) error {
	if len(envelope.GovernanceTaskIDs) == 0 || len(envelope.CompletionCriteria) == 0 || len(envelope.AllowedScopes) == 0 || len(envelope.AllowedSkills) == 0 || len(envelope.RoleAssignments) == 0 {
		return errors.New("mission tasks, completion criteria, scopes, skills, and assignments are required")
	}
	if hasBlankMissionStrings(envelope.GovernanceTaskIDs) || hasBlankMissionStrings(envelope.CompletionCriteria) || hasBlankMissionStrings(envelope.AllowedScopes) || hasBlankMissionSkills(envelope.AllowedSkills) || hasBlankMissionAssignments(envelope.RoleAssignments) {
		return errors.New("mission tasks, completion criteria, scopes, skills, and assignments cannot contain blanks")
	}
	return nil
}

func (envelope MissionEnvelope) CanonicalJSON() []byte {
	canonical, err := CanonicalizeMissionEnvelope(envelope)
	if err != nil {
		return nil
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil
	}
	return encoded
}

func canonicalStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func canonicalSkills(values []MissionSkillGrant) []MissionSkillGrant {
	result := make([]MissionSkillGrant, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Name = strings.TrimSpace(value.Name)
		value.Version = strings.TrimSpace(value.Version)
		key := value.Name + "\x00" + value.Version
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Version < result[j].Version
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func canonicalAssignments(values map[AgentFunction]string) map[AgentFunction]string {
	result := make(map[AgentFunction]string, len(values))
	for function, id := range values {
		result[function] = strings.TrimSpace(id)
	}
	return result
}

func hasBlankMissionStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func hasBlankMissionSkills(values []MissionSkillGrant) bool {
	for _, value := range values {
		if strings.TrimSpace(value.Name) == "" || strings.TrimSpace(value.Version) == "" {
			return true
		}
	}
	return false
}

func hasBlankMissionAssignments(values map[AgentFunction]string) bool {
	for function, id := range values {
		if strings.TrimSpace(string(function)) == "" || strings.TrimSpace(id) == "" {
			return true
		}
	}
	return false
}

func normalizedMissionCapabilitySet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

type AgentIdentityRegistered struct {
	Agent LogicalAgent `json:"agent"`
}

type RuntimeBound struct {
	Binding RuntimeBinding `json:"binding"`
}

type CapsuleImported struct {
	PreviewHash string         `json:"preview_hash"`
	Binding     RuntimeBinding `json:"binding"`
}

type RuntimeUnbound struct {
	LogicalActorID string `json:"logical_actor_id"`
	Revision       int    `json:"revision"`
}

type MissionIssued struct {
	Envelope MissionEnvelope `json:"envelope"`
}

type MissionInvalidated struct {
	MissionID string `json:"mission_id"`
	Reason    string `json:"reason"`
}

type ApprovalRequested struct {
	Approval ApprovalRequest `json:"approval"`
}

type ApprovalDecided struct {
	ApprovalID     string    `json:"approval_id"`
	PayloadSHA256  string    `json:"payload_sha256"`
	Decision       string    `json:"decision"`
	DecisionReason string    `json:"decision_reason"`
	DeciderID      string    `json:"decider_id"`
	DecidedAt      time.Time `json:"decided_at"`
}

type ApprovalInvalidated struct {
	ApprovalID string `json:"approval_id"`
	Reason     string `json:"reason"`
}

package mission

import (
	"errors"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

func Build(input BuildInput) (Envelope, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.ProjectID) == "" || input.GoalVersion <= 0 {
		return Envelope{}, errors.New("mission id, project id, and goal version are required")
	}
	if input.Context.ID == "" || input.Context.SliceHash == "" || input.Context.Superseded || input.Context.GoalVersion != input.GoalVersion {
		return Envelope{}, errors.New("mission context must be current and hash-bound to the goal version")
	}
	if input.Lease.ID != "" && (input.Lease.Status != "active" || input.Lease.GoalVersion != input.GoalVersion || input.Lease.ContextID != input.Context.ID || input.Lease.EnvironmentID != input.EnvironmentID) {
		return Envelope{}, errors.New("mission lease must be active and match context, environment, and goal version")
	}
	if input.Lease.ID == "" {
		return Envelope{}, errors.New("mission lease is required")
	}
	if strings.TrimSpace(input.RiskLevel) == "" || strings.TrimSpace(input.EnvironmentID) == "" || strings.TrimSpace(input.PolicyVersion) == "" || input.IssuedAt.IsZero() || input.Deadline.IsZero() || !input.Deadline.After(input.IssuedAt) {
		return Envelope{}, errors.New("mission risk, environment, policy, issue time, and future deadline are required")
	}
	if !model.IsValidRiskLevel(strings.TrimSpace(input.RiskLevel)) {
		return Envelope{}, errors.New("mission risk level is invalid")
	}
	if len(input.TaskIDs) != 1 {
		return Envelope{}, errors.New("mission requires exactly one task binding")
	}
	build, buildOK := input.Assignments[model.FunctionBuild]
	verify, verifyOK := input.Assignments[model.FunctionVerify]
	if !buildOK || !verifyOK || strings.TrimSpace(build) == "" || strings.TrimSpace(verify) == "" || build == verify {
		return Envelope{}, errors.New("build and verify must be assigned to distinct logical agents")
	}
	if input.Context.TaskID != input.TaskIDs[0] || input.Lease.TaskID != input.TaskIDs[0] {
		return Envelope{}, errors.New("mission context and lease task bindings must match")
	}
	if !model.IsMissionLeaseBoundToBuildAssignment(input.Lease, build) {
		return Envelope{}, errors.New("mission lease subject is not bound to the build assignment")
	}

	envelope := model.MissionEnvelope{
		ID:                 strings.TrimSpace(input.ID),
		ProjectID:          strings.TrimSpace(input.ProjectID),
		ContextID:          input.Context.ID,
		ContextHash:        input.Context.SliceHash,
		LeaseID:            input.Lease.ID,
		PolicyVersion:      strings.TrimSpace(input.PolicyVersion),
		GoalVersion:        input.GoalVersion,
		GovernanceTaskIDs:  input.TaskIDs,
		CompletionCriteria: input.CompletionCriteria,
		AllowedScopes:      input.AllowedScopes,
		AllowedSkills:      input.Skills,
		RoleAssignments:    input.Assignments,
		RiskLevel:          strings.TrimSpace(input.RiskLevel),
		EnvironmentID:      strings.TrimSpace(input.EnvironmentID),
		IssuedAt:           input.IssuedAt.UTC(),
		Deadline:           input.Deadline.UTC(),
	}
	if err := model.ValidateMissionEnvelopeContent(envelope); err != nil {
		return Envelope{}, err
	}
	if !model.MissionCapabilitiesWithinLease(input.Lease, envelope.AllowedScopes, envelope.AllowedSkills) {
		return Envelope{}, errors.New("mission capabilities exceed the lease")
	}
	canonical, err := model.CanonicalizeMissionEnvelope(envelope)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope(canonical), nil
}

func (envelope Envelope) CanonicalJSON() []byte {
	return model.MissionEnvelope(envelope).CanonicalJSON()
}

package agentteamsbridge

import (
	"context"
	"fmt"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

type Orchestrator interface {
	EnsureMissionTeam(context.Context, model.MissionEnvelope) (RuntimeTopology, error)
	StopMissionTeam(context.Context, string) error
}

type MissionOrchestrator struct{ Control ControlPlane }

// OfficialMissionOrchestrator provisions the v1.2.2 CRD topology without
// depending on the retired private control-plane API. Production wiring moves
// to this contract in Task 5; the legacy orchestrator remains only as a
// migration compatibility path until then.
type OfficialMissionOrchestrator struct {
	Control OfficialControlPlane
	Config  OfficialResourceConfig
}

func (orchestrator OfficialMissionOrchestrator) EnsureMissionTeam(ctx context.Context, envelope model.MissionEnvelope) (RuntimeTopology, error) {
	if orchestrator.Control == nil {
		return RuntimeTopology{}, fmt.Errorf("official AgentTeams control plane is required")
	}
	if _, err := orchestrator.Control.Discover(ctx); err != nil {
		return RuntimeTopology{}, err
	}
	resources, err := RenderOfficialMissionResources(envelope, orchestrator.Config)
	if err != nil {
		return RuntimeTopology{}, err
	}
	if _, err := orchestrator.Control.ApplyHuman(ctx, resources.Human); err != nil {
		return RuntimeTopology{}, err
	}
	if _, err := orchestrator.Control.ApplyManager(ctx, resources.Manager); err != nil {
		return RuntimeTopology{}, err
	}
	for _, worker := range resources.Workers {
		if _, err := orchestrator.Control.ApplyWorker(ctx, worker); err != nil {
			return RuntimeTopology{}, err
		}
	}
	if _, err := orchestrator.Control.ApplyTeam(ctx, resources.Team); err != nil {
		return RuntimeTopology{}, err
	}
	return orchestrator.Control.GetTopology(ctx, resources.Team.Object.GetName())
}

func (orchestrator OfficialMissionOrchestrator) StopMissionTeam(ctx context.Context, missionID string) error {
	if orchestrator.Control == nil || strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("official AgentTeams control plane and mission id are required")
	}
	return orchestrator.Control.StopMissionTeam(ctx, missionID)
}

func (orchestrator MissionOrchestrator) EnsureMissionTeam(ctx context.Context, envelope model.MissionEnvelope) (RuntimeTopology, error) {
	if orchestrator.Control == nil {
		return RuntimeTopology{}, fmt.Errorf("AgentTeams control plane is required")
	}
	profile, err := orchestrator.Control.Detect(ctx)
	if err != nil {
		return RuntimeTopology{}, err
	}
	if !isLegacyControlProfile(profile) {
		return RuntimeTopology{}, fmt.Errorf("%w: got %s %s", ErrUnsupportedProfile, profile.Name, profile.Version)
	}
	resources, err := RenderResources(envelope)
	if err != nil {
		return RuntimeTopology{}, err
	}
	for _, resource := range resources {
		if err := orchestrator.Control.Apply(ctx, resource); err != nil {
			return RuntimeTopology{}, err
		}
	}
	status, err := orchestrator.Control.GetTeam(ctx, resources[1].Name)
	if err != nil {
		return RuntimeTopology{}, err
	}
	if !strings.EqualFold(status.Phase, "Ready") {
		return RuntimeTopology{}, fmt.Errorf("AgentTeams team %q is not ready", resources[1].Name)
	}
	principals := status.RuntimePrincipalIDs
	return RuntimeTopology{MissionID: envelope.ID, TeamName: resources[1].Name, ManagerPrincipalID: principals[string(model.FunctionManager)], LeaderPrincipalID: principals[string(model.FunctionDeliveryLeader)], WorkerPrincipalIDs: map[model.AgentFunction]string{model.FunctionResearch: principals[string(model.FunctionResearch)], model.FunctionBuild: principals[string(model.FunctionBuild)], model.FunctionVerify: principals[string(model.FunctionVerify)]}, HumanPrincipalID: principals["human"], ManagerRoomID: status.LeaderRoomID, LeaderRoomID: status.LeaderRoomID, TeamRoomID: status.TeamRoomID}, nil
}

func (orchestrator MissionOrchestrator) StopMissionTeam(ctx context.Context, missionID string) error {
	if orchestrator.Control == nil || strings.TrimSpace(missionID) == "" {
		return fmt.Errorf("AgentTeams control plane and mission id are required")
	}
	_, err := orchestrator.Control.StopTeam(ctx, "haowork-"+strings.ToLower(strings.TrimSpace(missionID)))
	return err
}

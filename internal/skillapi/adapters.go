package skillapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/patchskills"
	"github.com/haochase/haowork/internal/skillruntime"
)

// AdapterMap dispatches a runtime-authorized invocation without giving the API layer event-store access.
type AdapterMap map[string]skillruntime.Adapter
type CrossZoneConfig struct {
	Signer                                                                   patchskills.Signer
	BuildAgentID, VerifyAgentID, AuditCommand, WorkspaceDigest, ArtifactHash string
	AuditExitCode                                                            int
}

func (adapters AdapterMap) Invoke(ctx context.Context, invocation skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	adapter := adapters[invocation.SkillName]
	if adapter == nil {
		return nil, nil, errors.New("skill adapter is not configured")
	}
	return adapter.Invoke(ctx, invocation)
}

// SignedCrossZoneAdapters creates outbound request adapters with no access to the peer environment.
// Callers must inject a signer; CoreAdapters remains fail-closed until then.
func SignedCrossZoneAdapters(signer patchskills.Signer, buildAgentID, verifyAgentID string) AdapterMap {
	return SignedCrossZoneAdaptersWithConfig(CrossZoneConfig{Signer: signer, BuildAgentID: buildAgentID, VerifyAgentID: verifyAgentID})
}
func SignedCrossZoneAdaptersWithConfig(config CrossZoneConfig) AdapterMap {
	adapters := make(AdapterMap, 4)
	for _, name := range []string{"advisory", "mirror", "patch", "audit"} {
		adapters[name] = patchskills.Adapter{Name: name, Signer: config.Signer, BuildAgentID: config.BuildAgentID, VerifyAgentID: config.VerifyAgentID, AuditCommand: config.AuditCommand, WorkspaceDigest: config.WorkspaceDigest, ArtifactHash: config.ArtifactHash, AuditExitCode: config.AuditExitCode}
	}
	return adapters
}

type ContextAdapter struct{ Service *app.Service }
type HistoryAdapter struct{ Service *app.Service }

// CoreAdapters is assembled by core.Project. Cross-zone and transfer capabilities fail closed until their owners inject implementations.
func CoreAdapters(service *app.Service) AdapterMap {
	return AdapterMap{
		"plan":     UnavailableAdapter{Code: "core_contract_not_configured"},
		"context":  ContextAdapter{Service: service},
		"record":   UnavailableAdapter{Code: "core_contract_not_configured"},
		"history":  HistoryAdapter{Service: service},
		"verify":   UnavailableAdapter{Code: "core_contract_not_configured"},
		"export":   UnavailableAdapter{Code: "transfer_not_configured"},
		"import":   UnavailableAdapter{Code: "transfer_not_configured"},
		"advisory": UnavailableAdapter{Code: "cross_zone_not_configured"},
		"mirror":   UnavailableAdapter{Code: "cross_zone_not_configured"},
		"patch":    UnavailableAdapter{Code: "cross_zone_not_configured"},
		"audit":    UnavailableAdapter{Code: "cross_zone_not_configured"},
	}
}
func CoreAdaptersWithCrossZone(service *app.Service, config CrossZoneConfig) AdapterMap {
	adapters := CoreAdapters(service)
	if config.Signer == nil || config.BuildAgentID == "" || config.VerifyAgentID == "" || config.AuditCommand == "" || config.WorkspaceDigest == "" || config.ArtifactHash == "" {
		return adapters
	}
	for name, adapter := range SignedCrossZoneAdaptersWithConfig(config) {
		adapters[name] = adapter
	}
	return adapters
}

func (adapter ContextAdapter) Invoke(ctx context.Context, invocation skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	slice, err := serviceContext(ctx, adapter.Service, invocation.ContextID)
	if err != nil {
		return nil, nil, err
	}
	return marshalOutput(map[string]string{"context_id": slice.ID})
}

func (adapter HistoryAdapter) Invoke(ctx context.Context, invocation skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	if adapter.Service == nil {
		return nil, nil, errors.New("core service is required")
	}
	events, err := adapter.Service.History(ctx, invocation.TaskID)
	if err != nil {
		return nil, nil, err
	}
	return marshalOutput(map[string]any{"events": events})
}

// UnavailableAdapter makes deferred transfer and cross-zone capabilities fail closed.
type UnavailableAdapter struct{ Code string }

func (adapter UnavailableAdapter) Invoke(context.Context, skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	if adapter.Code == "" {
		adapter.Code = "adapter_not_configured"
	}
	return nil, nil, errors.New(adapter.Code)
}

func serviceContext(ctx context.Context, service *app.Service, contextID string) (model.ContextSlice, error) {
	if service == nil {
		return model.ContextSlice{}, errors.New("core service is required")
	}
	return service.GetContext(ctx, contextID)
}

func marshalOutput(value any) (json.RawMessage, []model.ArtifactRef, error) {
	output, err := json.Marshal(value)
	return output, nil, err
}

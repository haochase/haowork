package transferhost

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
)

type FileProvider struct {
	Path string
}

func (provider FileProvider) Load(ctx context.Context, _ string) (*core.TransferConfig, error) {
	document, base, err := provider.loadDocument(ctx)
	if err != nil {
		return nil, err
	}
	privatePath, err := resolveConfigPath(base, document.SigningKey.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	privateKey, err := LoadPrivateKey(privatePath)
	if err != nil {
		return nil, err
	}
	publicKeys := make(map[string]ed25519.PublicKey, len(document.TrustedPublicKeys))
	for _, trusted := range document.TrustedPublicKeys {
		trustedPath, err := resolveConfigPath(base, trusted.PublicKeyFile)
		if err != nil {
			return nil, err
		}
		key, err := LoadPublicKey(trustedPath)
		if err != nil {
			return nil, err
		}
		publicKeys[trusted.KeyID] = key
	}
	bindingsPath, err := resolveConfigPath(base, document.RuntimeBindingsFile)
	if err != nil {
		return nil, err
	}
	bindings, err := loadRuntimeBindings(bindingsPath, document.EnvironmentID)
	if err != nil {
		return nil, err
	}
	provenancePath, err := resolveConfigPath(base, document.ProvenanceFile)
	if err != nil {
		return nil, err
	}
	verifiers, err := loadProvenanceVerifiers(provenancePath)
	if err != nil {
		return nil, err
	}
	ids := idgen.New()
	return &core.TransferConfig{
		TargetEnvironmentID: document.EnvironmentID,
		PublicKeys:          publicKeys,
		ExpectedGoalVersion: document.Expected.GoalVersion,
		ExpectedGitBaseline: strings.TrimSpace(document.Expected.GitBaseline),
		ExpectedContextHash: strings.TrimSpace(document.Expected.ContextHash),
		ExpectedLeaseID:     strings.TrimSpace(document.Expected.LeaseID),
		ExpectedScope:       append([]string(nil), document.Expected.Scope...),
		RequiredSkills:      cloneStrings(document.Expected.RequiredSkills),
		ReturnSigner:        transfer.NewEd25519Signer(document.SigningKey.KeyID, privateKey),
		ReturnTTL:           time.Duration(document.ReturnTTLSeconds) * time.Second,
		RuntimeBindingResolver: transfer.RuntimeBindingResolverFunc(func(ctx context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
			if err := ctx.Err(); err != nil {
				return model.RuntimeBinding{}, err
			}
			binding, ok := bindings[bindingKey(logicalID, environmentID)]
			if !ok {
				return model.RuntimeBinding{}, errors.New("target runtime binding is not configured")
			}
			return binding, nil
		}),
		ProvenanceVerifiers: verifiers,
		NewEventID: func() string {
			value, err := ids.New("EVT")
			if err != nil {
				return ""
			}
			return value
		},
	}, nil
}

func (provider FileProvider) BootstrapProject(ctx context.Context, projectRoot string, actor model.Actor) ([]model.RuntimeBinding, error) {
	document, base, err := provider.loadDocument(ctx)
	if err != nil {
		return nil, err
	}
	bindingsPath, err := resolveConfigPath(base, document.RuntimeBindingsFile)
	if err != nil {
		return nil, err
	}
	_, agents, bindings, err := loadRuntimeTopology(bindingsPath, document.EnvironmentID)
	if err != nil {
		return nil, err
	}
	project, err := core.Open(ctx, projectRoot, core.Dependencies{IDs: idgen.New(), Clock: wallClock{}})
	if err != nil {
		return nil, err
	}
	return project.Service.BootstrapRuntimeTopology(ctx, agents, bindings, actor)
}

func (provider FileProvider) loadDocument(ctx context.Context) (hostConfig, string, error) {
	if err := ctx.Err(); err != nil {
		return hostConfig{}, "", err
	}
	path := strings.TrimSpace(provider.Path)
	if path == "" {
		return hostConfig{}, "", errors.New("trusted transfer Core config path is required")
	}
	var document hostConfig
	if err := decodeOwnerOnlyJSON(path, &document); err != nil {
		return hostConfig{}, "", err
	}
	normalizeHostConfig(&document)
	if err := validateHostConfig(document); err != nil {
		return hostConfig{}, "", err
	}
	return document, filepath.Dir(path), nil
}

func normalizeHostConfig(document *hostConfig) {
	document.EnvironmentID = strings.TrimSpace(document.EnvironmentID)
	document.SigningKey.KeyID = strings.TrimSpace(document.SigningKey.KeyID)
	document.SigningKey.PrivateKeyFile = strings.TrimSpace(document.SigningKey.PrivateKeyFile)
	for index := range document.TrustedPublicKeys {
		document.TrustedPublicKeys[index].KeyID = strings.TrimSpace(document.TrustedPublicKeys[index].KeyID)
		document.TrustedPublicKeys[index].PublicKeyFile = strings.TrimSpace(document.TrustedPublicKeys[index].PublicKeyFile)
	}
	document.RuntimeBindingsFile = strings.TrimSpace(document.RuntimeBindingsFile)
	document.ProvenanceFile = strings.TrimSpace(document.ProvenanceFile)
}

func validateHostConfig(document hostConfig) error {
	document.EnvironmentID = strings.TrimSpace(document.EnvironmentID)
	document.SigningKey.KeyID = strings.TrimSpace(document.SigningKey.KeyID)
	if document.Version != 1 || document.EnvironmentID == "" || document.SigningKey.KeyID == "" || strings.TrimSpace(document.SigningKey.PrivateKeyFile) == "" || len(document.TrustedPublicKeys) == 0 || strings.TrimSpace(document.RuntimeBindingsFile) == "" || strings.TrimSpace(document.ProvenanceFile) == "" || document.Expected.GoalVersion < 1 {
		return errors.New("trusted transfer Core configuration is incomplete")
	}
	if document.ReturnTTLSeconds != 0 && (document.ReturnTTLSeconds < 3600 || document.ReturnTTLSeconds > 7*24*3600) {
		return errors.New("trusted transfer Core return TTL must be between one hour and seven days")
	}
	seen := make(map[string]struct{}, len(document.TrustedPublicKeys))
	for _, trusted := range document.TrustedPublicKeys {
		trusted.KeyID = strings.TrimSpace(trusted.KeyID)
		if trusted.KeyID == "" || strings.TrimSpace(trusted.PublicKeyFile) == "" {
			return errors.New("trusted public key configuration is incomplete")
		}
		if _, exists := seen[trusted.KeyID]; exists {
			return errors.New("trusted public key ID is duplicated")
		}
		seen[trusted.KeyID] = struct{}{}
	}
	for name, version := range document.Expected.RequiredSkills {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			return errors.New("required transfer skill is invalid")
		}
	}
	return nil
}

func loadRuntimeBindings(path, environmentID string) (map[string]model.RuntimeBinding, error) {
	result, _, _, err := loadRuntimeTopology(path, environmentID)
	return result, err
}

func loadRuntimeTopology(path, environmentID string) (map[string]model.RuntimeBinding, []model.LogicalAgent, []model.RuntimeBinding, error) {
	var document bindingDocument
	if err := decodeOwnerOnlyJSON(path, &document); err != nil {
		return nil, nil, nil, err
	}
	if document.Version != 1 || len(document.Bindings) == 0 {
		return nil, nil, nil, errors.New("runtime binding document is incomplete")
	}
	result := make(map[string]model.RuntimeBinding, len(document.Bindings))
	agents := make([]model.LogicalAgent, 0, len(document.Bindings))
	ordered := make([]model.RuntimeBinding, 0, len(document.Bindings))
	for _, value := range document.Bindings {
		binding := model.RuntimeBinding{
			LogicalActorID: strings.TrimSpace(value.LogicalActorID), Revision: value.Revision,
			EnvironmentID: strings.TrimSpace(value.EnvironmentID), AgentTeamsInstanceID: strings.TrimSpace(value.AgentTeamsInstanceID),
			RuntimePrincipalID: strings.TrimSpace(value.RuntimePrincipalID), HumanPrincipalID: strings.TrimSpace(value.HumanPrincipalID),
			LeaderRoomID: strings.TrimSpace(value.LeaderRoomID), TeamRoomID: strings.TrimSpace(value.TeamRoomID), Status: strings.TrimSpace(value.Status),
		}
		if binding.LogicalActorID == "" || !validAgentFunction(value.AgentFunction) || binding.Revision < 1 || binding.EnvironmentID != environmentID || binding.AgentTeamsInstanceID == "" || binding.RuntimePrincipalID == "" || binding.Status != "active" {
			return nil, nil, nil, errors.New("runtime binding is invalid")
		}
		key := bindingKey(binding.LogicalActorID, binding.EnvironmentID)
		if _, exists := result[key]; exists {
			return nil, nil, nil, errors.New("runtime binding is duplicated")
		}
		result[key] = binding
		agents = append(agents, model.LogicalAgent{ID: binding.LogicalActorID, SubjectKind: model.ActorAgent, GovernanceRole: model.RoleAgent, Function: value.AgentFunction, Status: "active"})
		ordered = append(ordered, binding)
	}
	return result, agents, ordered, nil
}

func validAgentFunction(value model.AgentFunction) bool {
	switch value {
	case model.FunctionManager, model.FunctionDeliveryLeader, model.FunctionResearch, model.FunctionBuild, model.FunctionVerify:
		return true
	default:
		return false
	}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }

func loadProvenanceVerifiers(path string) (transfer.ProvenanceVerifierSet, error) {
	var document provenanceDocument
	if err := decodeOwnerOnlyJSON(path, &document); err != nil {
		return nil, err
	}
	if document.Version != 1 || len(document.Entries) == 0 {
		return nil, errors.New("provenance allowlist is incomplete")
	}
	bySource := make(map[string]map[string]struct{})
	for _, entry := range document.Entries {
		if err := validateProvenanceEntry(entry); err != nil {
			return nil, err
		}
		if bySource[entry.Source] == nil {
			bySource[entry.Source] = make(map[string]struct{})
		}
		key := provenanceKey(entry.Source, entry.Path, entry.SHA256)
		if _, exists := bySource[entry.Source][key]; exists {
			return nil, errors.New("provenance allowlist entry is duplicated")
		}
		bySource[entry.Source][key] = struct{}{}
	}
	result := make(transfer.ProvenanceVerifierSet, len(bySource))
	for source, allowed := range bySource {
		result[source] = exactProvenanceVerifier{allowed: allowed}
	}
	return result, nil
}

func bindingKey(logicalID, environmentID string) string {
	return strings.TrimSpace(logicalID) + "\x00" + strings.TrimSpace(environmentID)
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

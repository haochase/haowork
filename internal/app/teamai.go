package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/teamai"
)

type TeamAIDoctorReport struct {
	Configured bool                `json:"configured"`
	Status     string              `json:"status"`
	Doctor     teamai.DoctorResult `json:"doctor"`
}

type BindTeamAIInput struct {
	TeamRepo     string
	TeamRevision string
	Actor        model.Actor
}

type TeamAIVerifyReport struct {
	Binding model.TeamAIBinding `json:"binding"`
	Doctor  teamai.DoctorResult `json:"doctor"`
}

// DoctorTeamAI only reads configuration, official CLI diagnostics and local assets.
func DoctorTeamAI(ctx context.Context, root, assetRoot string, detector teamai.Detector) (TeamAIDoctorReport, error) {
	result := TeamAIDoctorReport{Status: "Unbound", Doctor: teamai.DoctorResult{Assets: []teamai.AssetResult{}, Checks: []teamai.DoctorCheck{}, Errors: []teamai.Issue{}}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	filename := filepath.Join(root, ".haowork", "teamai.yaml")
	if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	cfg, err := teamai.LoadConfig(filename)
	if err != nil {
		return result, err
	}
	result.Configured = true
	result.Status = "Configured"
	detector.ProjectRoot = root
	result.Doctor, err = detector.Doctor(ctx, teamAIAssetRoot(root, assetRoot), cfg)
	return result, err
}

func (s *Service) BindTeamAI(ctx context.Context, root string, input BindTeamAIInput) (model.TeamAIBinding, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := validateActor(input.Actor); err != nil {
		return model.TeamAIBinding{}, err
	}
	if !canApprove(input.Actor) {
		return model.TeamAIBinding{}, ErrApprovalRequired
	}
	cfg, err := teamai.LoadConfig(filepath.Join(root, ".haowork", "teamai.yaml"))
	if err != nil {
		return model.TeamAIBinding{}, err
	}
	if input.TeamRepo != cfg.TeamRepo || input.TeamRevision != cfg.TeamRevision {
		return model.TeamAIBinding{}, fmt.Errorf("%w: TeamAI flags do not match project configuration", ErrConflict)
	}
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return model.TeamAIBinding{}, err
	}
	for _, existing := range state.TeamAIBindings {
		if teamAIBindingMatches(existing, cfg) {
			return existing, nil
		}
		return model.TeamAIBinding{}, fmt.Errorf("%w: TeamAI binding differs from project configuration", ErrConflict)
	}
	id, err := s.ids.New("TAI")
	if err != nil {
		return model.TeamAIBinding{}, wrapOperational(err)
	}
	binding := model.TeamAIBinding{ID: id, ProjectID: state.ProjectID, TeamRepo: cfg.TeamRepo, TeamRevision: cfg.TeamRevision, Scope: cfg.Scope, RequiredAssets: teamAIAssetSpecs(cfg), BoundBy: input.Actor.ID, BoundAt: s.clock.Now().UTC(), Status: "Bound"}
	event, err := s.prepareSCMEvent("teamai.bound", "teamai_binding", id, input.Actor, model.TeamAIBound{Binding: binding})
	if err != nil {
		return model.TeamAIBinding{}, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return model.TeamAIBinding{}, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return model.TeamAIBinding{}, err
	}
	return binding, nil
}

// VerifyTeamAI proves listed local asset bytes and official initialization only.
// TeamRevision remains the declared target and is not a verified Git HEAD.
func (s *Service) VerifyTeamAI(ctx context.Context, root, assetRoot string, detector teamai.Detector) (TeamAIVerifyReport, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	result := TeamAIVerifyReport{}
	cfg, err := teamai.LoadConfig(filepath.Join(root, ".haowork", "teamai.yaml"))
	if err != nil {
		return result, err
	}
	state, history, err := s.snapshotWithEvents(ctx)
	if err != nil {
		return result, err
	}
	found := false
	for _, binding := range state.TeamAIBindings {
		if teamAIBindingMatches(binding, cfg) {
			result.Binding = binding
			found = true
			break
		}
	}
	if !found {
		return result, fmt.Errorf("%w: matching TeamAI binding is required", ErrConflict)
	}
	// Always re-read the real files, even if a detector has an injected FS.
	verified, err := teamai.Verify(ctx, root, cfg, assetRoot)
	if err != nil || !verified.Ready {
		if err == nil {
			err = ErrGateFailed
		}
		return result, err
	}
	detector.ProjectRoot = root
	result.Doctor, err = detector.Doctor(ctx, teamAIAssetRoot(root, assetRoot), cfg)
	if err != nil || !result.Doctor.Initialized {
		if err == nil {
			err = ErrGateFailed
		}
		return result, err
	}
	assets := make([]model.TeamAIAssetSpec, len(verified.Assets))
	for i, asset := range verified.Assets {
		assets[i] = model.TeamAIAssetSpec{Path: asset.Path, Kind: asset.Kind, SHA256: strings.ToLower(asset.SHA256)}
	}
	sortTeamAIAssets(assets)
	canonical, err := json.Marshal(assets)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	if result.Binding.Status == "Ready" && result.Binding.AssetDigest == digest {
		return result, nil
	}
	verifiedAt := s.clock.Now().UTC()
	event, err := s.prepareSCMEvent("teamai.assets.verified", "teamai_binding", result.Binding.ID, model.Actor{ID: "haowork-teamai-verifier", Kind: model.ActorAgent, Role: model.RoleAgent}, model.TeamAIAssetsVerified{BindingID: result.Binding.ID, AssetDigest: digest, VerifiedAt: verifiedAt})
	if err != nil {
		return result, err
	}
	if err := validateSCMPrepared(history, event); err != nil {
		return result, err
	}
	if err := s.appendPreparedEvent(ctx, len(history), event); err != nil {
		return result, err
	}
	result.Binding.Status = "Ready"
	result.Binding.AssetDigest = digest
	result.Binding.VerifiedAt = verifiedAt
	return result, nil
}

func teamAIAssetRoot(root, assetRoot string) string {
	if filepath.IsAbs(assetRoot) {
		return assetRoot
	}
	return filepath.Join(root, assetRoot)
}

func teamAIAssetSpecs(cfg teamai.Config) []model.TeamAIAssetSpec {
	assets := make([]model.TeamAIAssetSpec, len(cfg.RequiredAssets))
	for i, asset := range cfg.RequiredAssets {
		assets[i] = model.TeamAIAssetSpec{Path: asset.Path, Kind: asset.Kind, SHA256: strings.ToLower(asset.SHA256)}
	}
	sortTeamAIAssets(assets)
	return assets
}

func sortTeamAIAssets(assets []model.TeamAIAssetSpec) {
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].Path != assets[j].Path {
			return assets[i].Path < assets[j].Path
		}
		if assets[i].Kind != assets[j].Kind {
			return assets[i].Kind < assets[j].Kind
		}
		return assets[i].SHA256 < assets[j].SHA256
	})
}

func teamAIBindingMatches(binding model.TeamAIBinding, cfg teamai.Config) bool {
	assets := append([]model.TeamAIAssetSpec(nil), binding.RequiredAssets...)
	sortTeamAIAssets(assets)
	return binding.TeamRepo == cfg.TeamRepo && binding.TeamRevision == cfg.TeamRevision && binding.Scope == cfg.Scope && reflect.DeepEqual(assets, teamAIAssetSpecs(cfg))
}

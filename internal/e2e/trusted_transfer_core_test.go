package e2e_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
	"github.com/haochase/haowork/internal/transfer"
	"github.com/haochase/haowork/internal/transferhost"
)

func TestTrustedTransferCoreCompletesSignedPublicInternalRoundTrip(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 3, 1, 0, 0, 0, time.UTC)
	root := t.TempDir()
	publicRoot := initializeTrustedProject(t, filepath.Join(root, "public"), now)
	internalRoot := initializeTrustedProject(t, filepath.Join(root, "internal"), now)

	publicPrivate, publicPublic := generateTrustedPair(t, filepath.Join(root, "public-host"), "public")
	internalPrivate, internalPublic := generateTrustedPair(t, filepath.Join(root, "internal-host"), "internal")
	publicEntry := trustedGitEntry(`{"patch":"public-approved"}`, "git/diff/public.json")
	internalEntry := trustedGitEntry(`{"patch":"internal-approved"}`, "git/diff/internal.json")
	publicConfigPath := writeTrustedHostConfig(t, filepath.Join(root, "public-host"), "public", "public-key", publicPrivate, "internal-key", internalPublic, "runtime-public-build", publicEntry)
	internalConfigPath := writeTrustedHostConfig(t, filepath.Join(root, "internal-host"), "internal", "internal-key", internalPrivate, "public-key", publicPublic, "runtime-internal-build", internalEntry)

	publicProvider := transferhost.FileProvider{Path: publicConfigPath}
	internalProvider := transferhost.FileProvider{Path: internalConfigPath}
	if _, err := publicProvider.BootstrapProject(ctx, publicRoot, trustedOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := internalProvider.BootstrapProject(ctx, internalRoot, trustedOwner); err != nil {
		t.Fatal(err)
	}
	publicConfig, err := publicProvider.Load(ctx, publicRoot)
	if err != nil {
		t.Fatal(err)
	}
	internalConfig, err := internalProvider.Load(ctx, internalRoot)
	if err != nil {
		t.Fatal(err)
	}
	publicProject := openTrustedProject(t, publicRoot, publicConfig, now)
	internalProject := openTrustedProject(t, internalRoot, internalConfig, now)

	manifest := trustedManifest(now)
	archive, err := transfer.ExportBytes(transfer.ExportInput{Manifest: manifest, Entries: []transfer.Entry{publicEntry}, Signer: publicProject.Transfer.ReturnSigner, ProvenanceVerifier: publicProject.Transfer.ProvenanceVerifier})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := publicProject.Transfer.PreviewImport(ctx, archive); err == nil {
		t.Fatal("public Core accepted a capsule targeting internal")
	}
	wrongKeyService := *internalProject.Transfer
	wrongPublicKey, err := transferhost.LoadPublicKey(internalPublic)
	if err != nil {
		t.Fatal(err)
	}
	wrongKeyService.PublicKeys = map[string]ed25519.PublicKey{"public-key": wrongPublicKey}
	if _, err := wrongKeyService.PreviewImport(ctx, archive); err == nil {
		t.Fatal("internal Core accepted a capsule signed by an untrusted key")
	}
	tampered := append([]byte(nil), archive...)
	tampered[len(tampered)-1] ^= 1
	if _, err := internalProject.Transfer.PreviewImport(ctx, tampered); err == nil {
		t.Fatal("internal Core accepted a tampered capsule")
	}
	missingBindingService := *internalProject.Transfer
	missingBindingService.RuntimeBindingResolver = transfer.RuntimeBindingResolverFunc(func(context.Context, string, string) (model.RuntimeBinding, error) {
		return model.RuntimeBinding{}, errors.New("missing")
	})
	if _, err := missingBindingService.PreviewImport(ctx, archive); err == nil {
		t.Fatal("internal Core accepted a capsule without a target runtime binding")
	}

	internalPreview, err := internalProject.Transfer.PreviewImport(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	if got := internalPreview.RebindRequired[0].RuntimePrincipalID; got != "runtime-internal-build" {
		t.Fatalf("internal runtime principal = %q", got)
	}
	if err := internalProject.Transfer.ApplyImport(ctx, internalPreview, transfer.Approval{PreviewHash: internalPreview.PreviewHash, Actor: trustedOwner}); err != nil {
		t.Fatal(err)
	}

	request := transfer.ReturnRequest{
		Base:      manifest,
		Current:   transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-TRUSTED", Scope: []string{"src/**"}, GitBaseline: "commit-public", DesignHash: "design-a", EvidenceHash: "evidence-a"},
		Candidate: transfer.ReturnState{GoalVersion: 1, LeaseID: "LSE-TRUSTED", Scope: []string{"src/**"}, GitBaseline: "commit-public", DesignHash: "design-a", EvidenceHash: "evidence-b"},
		Changes:   []transfer.ApprovedChange{{Entry: internalEntry}}, ApprovedEntryHashes: []string{transfer.EntryApprovalHash(internalEntry)},
	}
	payloadHash := transfer.ReturnApprovalHash(request)
	requester := model.Actor{ID: "AGT-BUILD", Kind: model.ActorAgent, Role: model.RoleAgent}
	approval, err := internalProject.Service.RequestApproval(ctx, "transfer", manifest.TransferID, payloadHash, "L3", requester)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := internalProject.Service.DecideApproval(ctx, approval.ID, payloadHash, "approved", "verified internal delta", trustedOwner); err != nil {
		t.Fatal(err)
	}
	request.Approval = transfer.ReturnApproval{ID: approval.ID, PayloadSHA256: payloadHash}
	returned, err := internalProject.Transfer.BuildReturn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Manifest.SourceEnvironmentID != "internal" || returned.Manifest.TargetEnvironmentID != "public" || len(returned.Conflicts) != 2 {
		t.Fatalf("return delta = %#v", returned)
	}

	publicPreview, err := publicProject.Transfer.PreviewImport(ctx, returned.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if got := publicPreview.RebindRequired[0].RuntimePrincipalID; got != "runtime-public-build" {
		t.Fatalf("public runtime principal = %q", got)
	}
	if err := publicProject.Transfer.ApplyImport(ctx, publicPreview, transfer.Approval{PreviewHash: publicPreview.PreviewHash, Actor: trustedOwner}); err != nil {
		t.Fatal(err)
	}
}

var trustedOwner = model.Actor{ID: "USR-TRUSTED-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}

func initializeTrustedProject(t *testing.T, root string, now time.Time) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "trusted-transfer", ProjectID: "PRJ-TRUSTED", Goal: "complete signed cross-zone handoff", CompletionCriteria: []string{"signed return merged"}, Actor: trustedOwner,
	}, &testkit.IDs{}, testkit.Clock{Value: now})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func generateTrustedPair(t *testing.T, root, name string) (string, string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, name+"-private.pem")
	publicPath := filepath.Join(root, name+"-public.pem")
	if err := transferhost.GenerateKeyPair(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	return privatePath, publicPath
}

func writeTrustedHostConfig(t *testing.T, root, environmentID, keyID, privatePath, trustedKeyID, trustedPublicPath, runtimePrincipal string, entry transfer.Entry) string {
	t.Helper()
	bindingsPath := filepath.Join(root, "runtime-bindings.json")
	provenancePath := filepath.Join(root, "provenance.json")
	configPath := filepath.Join(root, "transfer-host.json")
	writeTrustedJSON(t, bindingsPath, map[string]any{"version": 1, "bindings": []map[string]any{{"logical_actor_id": "AGT-BUILD", "agent_function": "build", "revision": 1, "environment_id": environmentID, "agentteams_instance_id": "default", "runtime_principal_id": runtimePrincipal, "status": "active"}}})
	writeTrustedJSON(t, provenancePath, map[string]any{"version": 1, "entries": []map[string]any{{"source": entry.Provenance.Source, "path": entry.Path, "sha256": entry.Provenance.SHA256}}})
	writeTrustedJSON(t, configPath, map[string]any{
		"version": 1, "environment_id": environmentID,
		"signing_key":           map[string]any{"key_id": keyID, "private_key_file": privatePath},
		"trusted_public_keys":   []map[string]any{{"key_id": trustedKeyID, "public_key_file": trustedPublicPath}},
		"runtime_bindings_file": bindingsPath, "provenance_file": provenancePath,
		"expected": map[string]any{"goal_version": 1, "git_baseline": "commit-public", "context_hash": "context-trusted", "lease_id": "LSE-TRUSTED", "scope": []string{"src/**"}, "required_skills": map[string]string{"patch": "1.0.0"}},
	})
	return configPath
}

func writeTrustedJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transferhost.ProtectOwnerOnlyFile(path); err != nil {
		t.Fatal(err)
	}
}

func openTrustedProject(t *testing.T, root string, config *core.TransferConfig, now time.Time) core.Project {
	t.Helper()
	project, err := core.Open(context.Background(), root, core.Dependencies{IDs: idgen.New(), Clock: testkit.Clock{Value: now}, Transfer: config})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func trustedManifest(now time.Time) transfer.Manifest {
	return transfer.Manifest{
		ProtocolVersion: transfer.ProtocolVersion, TransferID: "XFR-TRUSTED-001", ProjectID: "PRJ-TRUSTED", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", GoalVersion: 1,
		MissionIDs: []string{"MSN-TRUSTED"}, GovernanceEventIDs: []string{"EVT-TRUSTED"}, TraceIDs: []string{"TRC-TRUSTED"}, TaskIDs: []string{"TSK-TRUSTED"}, WorkItemIDs: []string{"WKI-TRUSTED"}, RunIDs: []string{"RUN-TRUSTED"},
		ContextID: "CTX-TRUSTED", ContextHash: "context-trusted", LeaseID: "LSE-TRUSTED", Scope: []string{"src/**"}, Skills: []transfer.SkillRef{{Name: "patch", Version: "1.0.0"}}, GitBaseline: "commit-public",
		Agents:    []transfer.RuntimeHistory{{LogicalActorID: "AGT-BUILD", Bindings: []model.RuntimeBinding{{LogicalActorID: "AGT-BUILD", Revision: 1, EnvironmentID: "public", RuntimePrincipalID: "runtime-public-build", AgentTeamsInstanceID: "default", Status: "active"}}}},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func trustedGitEntry(data, path string) transfer.Entry {
	digest := sha256.Sum256([]byte(data))
	return transfer.Entry{Type: transfer.EntryGitDiff, Path: path, Data: []byte(data), Provenance: transfer.EntryProvenance{Source: "git", SHA256: hex.EncodeToString(digest[:])}}
}

package transferhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
	"github.com/haochase/haowork/internal/transfer"
)

func TestFileProviderLoadsSignerTrustBindingsAndProvenance(t *testing.T) {
	fixture := newProviderFixture(t)
	config, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if config.TargetEnvironmentID != "internal" || config.ReturnSigner.KeyID() != "internal-key" {
		t.Fatalf("transfer config identity = %#v, %q", config.TargetEnvironmentID, config.ReturnSigner.KeyID())
	}
	if config.ReturnTTL != 24*time.Hour {
		t.Fatalf("return TTL = %s, want 24h", config.ReturnTTL)
	}
	if len(config.PublicKeys) != 1 || len(config.PublicKeys["public-key"]) == 0 {
		t.Fatalf("trusted public keys = %#v", config.PublicKeys)
	}
	binding, err := config.RuntimeBindingResolver.ResolveRuntimeBinding(context.Background(), "AGT-BUILD", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if binding.RuntimePrincipalID != "@build:internal.matrix.local" || binding.AgentTeamsInstanceID != "default" {
		t.Fatalf("runtime binding = %#v", binding)
	}
	entry := transfer.Entry{Type: transfer.EntryGitDiff, Path: fixture.entryPath, Data: fixture.entryData, Provenance: transfer.EntryProvenance{Source: "git", SHA256: fixture.entryDigest}}
	if err := config.ProvenanceVerifiers.VerifyProvenance(context.Background(), entry); err != nil {
		t.Fatalf("trusted provenance = %v", err)
	}
	first := config.NewEventID()
	second := config.NewEventID()
	if first == "" || second == "" || first == second || !strings.HasPrefix(first, "EVT-") {
		t.Fatalf("event IDs = %q, %q", first, second)
	}
}

func TestFileProviderBootstrapsConfiguredLogicalAgentAndRuntimeBinding(t *testing.T) {
	fixture := newProviderFixture(t)
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	bindings, err := (FileProvider{Path: fixture.configPath}).BootstrapProject(context.Background(), fixture.projectRoot, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].Revision != 1 || bindings[0].RuntimePrincipalID != "@build:internal.matrix.local" {
		t.Fatalf("bootstrapped bindings = %#v", bindings)
	}
	events, err := eventstore.New(fixture.projectRoot).ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := model.Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.Agents["AGT-BUILD"].Function != model.FunctionBuild || len(state.RuntimeBindings["AGT-BUILD"]) != 1 {
		t.Fatalf("bootstrapped state = %#v, %#v", state.Agents, state.RuntimeBindings)
	}
}

func TestFileProviderRejectsUnknownOrTrailingConfiguration(t *testing.T) {
	fixture := newProviderFixture(t)
	content, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(content), `"version":1`, `"version":1,"unexpected":true`, 1)
	writeSecureFile(t, fixture.configPath, []byte(unknown))
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted an unknown configuration field")
	}

	writeSecureFile(t, fixture.configPath, append(content, []byte(` {}`)...))
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted trailing JSON")
	}
}

func TestFileProviderRejectsDisabledGoalVersionGate(t *testing.T) {
	fixture := newProviderFixture(t)
	content, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.Replace(string(content), `"goal_version":1`, `"goal_version":0`, 1))
	writeSecureFile(t, fixture.configPath, content)
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted goal_version 0 and disabled the import gate")
	}
}

func TestFileProviderRejectsUnsafeReturnTTL(t *testing.T) {
	for _, seconds := range []int{3599, 604801} {
		fixture := newProviderFixture(t)
		content, err := os.ReadFile(fixture.configPath)
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(strings.Replace(string(content), `"return_ttl_seconds":86400`, fmt.Sprintf(`"return_ttl_seconds":%d`, seconds), 1))
		writeSecureFile(t, fixture.configPath, content)
		if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
			t.Fatalf("FileProvider accepted unsafe return TTL %d", seconds)
		}
	}
}

func TestFileProviderRejectsConfigurationLargerThanLimit(t *testing.T) {
	fixture := newProviderFixture(t)
	content, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, bytes.Repeat([]byte(" "), maxConfigBytes)...)
	writeSecureFile(t, fixture.configPath, content)
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted a configuration larger than its limit")
	}
}

func TestFileProviderRejectsDuplicateTrustedKeyID(t *testing.T) {
	fixture := newProviderFixture(t)
	content, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := strings.Replace(string(content), `]`, fmt.Sprintf(`,{"key_id":"public-key","public_key_file":%q}]`, filepath.Base(fixture.publicKeyPath)), 1)
	writeSecureFile(t, fixture.configPath, []byte(duplicate))
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted duplicate trusted key ID")
	}
}

func TestFileProviderRejectsBindingForAnotherEnvironment(t *testing.T) {
	fixture := newProviderFixture(t)
	bindings := `{"version":1,"bindings":[{"logical_actor_id":"AGT-BUILD","revision":1,"environment_id":"public","agentteams_instance_id":"default","runtime_principal_id":"@build:public.matrix.local","status":"active"}]}`
	writeSecureFile(t, fixture.bindingsPath, []byte(bindings))
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted a runtime binding for another environment")
	}
}

func TestFileProviderResolverRejectsMissingLogicalActor(t *testing.T) {
	fixture := newProviderFixture(t)
	config, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.RuntimeBindingResolver.ResolveRuntimeBinding(context.Background(), "AGT-UNKNOWN", "internal"); err == nil {
		t.Fatal("runtime resolver invented a missing logical actor")
	}
}

func TestFileProviderProvenanceRequiresExactAllowlistEntry(t *testing.T) {
	fixture := newProviderFixture(t)
	config, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	entry := transfer.Entry{Type: transfer.EntryGitDiff, Path: fixture.entryPath, Data: []byte(`{"patch":"changed"}`), Provenance: transfer.EntryProvenance{Source: "git", SHA256: fixture.entryDigest}}
	if err := config.ProvenanceVerifiers.VerifyProvenance(context.Background(), entry); err == nil {
		t.Fatal("provenance verifier accepted bytes outside the exact allowlist")
	}
}

func TestFileProviderRejectsTraceLedgerInStaticProvenance(t *testing.T) {
	fixture := newProviderFixture(t)
	provenance := fmt.Sprintf(`{"version":1,"entries":[{"source":"trace-ledger","path":%q,"sha256":%q}]}`, fixture.entryPath, fixture.entryDigest)
	writeSecureFile(t, fixture.provenancePath, []byte(provenance))
	if _, err := (FileProvider{Path: fixture.configPath}).Load(context.Background(), fixture.projectRoot); err == nil {
		t.Fatal("FileProvider accepted a static trace-ledger verifier")
	}
}

type providerFixture struct {
	projectRoot, configPath, privateKeyPath, publicKeyPath string
	bindingsPath, provenancePath, entryPath                string
	entryData                                              []byte
	entryDigest                                            string
}

func newProviderFixture(t *testing.T) providerFixture {
	t.Helper()
	root := t.TempDir()
	_, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{Root: root, Name: "provider-fixture", ProjectID: "PRJ-PROVIDER", Goal: "signed transfer", CompletionCriteria: []string{"signed archive"}, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "internal-private.pem")
	localPublicPath := filepath.Join(root, "internal-public.pem")
	if err := GenerateKeyPair(privatePath, localPublicPath); err != nil {
		t.Fatal(err)
	}
	remotePrivatePath := filepath.Join(root, "public-private.pem")
	remotePublicPath := filepath.Join(root, "public-public.pem")
	if err := GenerateKeyPair(remotePrivatePath, remotePublicPath); err != nil {
		t.Fatal(err)
	}
	bindingsPath := filepath.Join(root, "runtime-bindings.json")
	provenancePath := filepath.Join(root, "provenance.json")
	configPath := filepath.Join(root, "transfer-host.json")
	entryPath := "git/diff/change.json"
	entryData := []byte(`{"patch":"approved"}`)
	digest := sha256.Sum256(entryData)
	entryDigest := hex.EncodeToString(digest[:])
	writeSecureFile(t, bindingsPath, []byte(`{"version":1,"bindings":[{"logical_actor_id":"AGT-BUILD","agent_function":"build","revision":1,"environment_id":"internal","agentteams_instance_id":"default","runtime_principal_id":"@build:internal.matrix.local","status":"active"}]}`))
	writeSecureFile(t, provenancePath, []byte(fmt.Sprintf(`{"version":1,"entries":[{"source":"git","path":%q,"sha256":%q}]}`, entryPath, entryDigest)))
	config := fmt.Sprintf(`{"version":1,"environment_id":"internal","return_ttl_seconds":86400,"signing_key":{"key_id":"internal-key","private_key_file":%q},"trusted_public_keys":[{"key_id":"public-key","public_key_file":%q}],"runtime_bindings_file":%q,"provenance_file":%q,"expected":{"goal_version":1,"git_baseline":"","context_hash":"","lease_id":"","scope":[],"required_skills":{}}}`, filepath.Base(privatePath), filepath.Base(remotePublicPath), filepath.Base(bindingsPath), filepath.Base(provenancePath))
	writeSecureFile(t, configPath, []byte(config))
	return providerFixture{projectRoot: root, configPath: configPath, privateKeyPath: privatePath, publicKeyPath: remotePublicPath, bindingsPath: bindingsPath, provenancePath: provenancePath, entryPath: entryPath, entryData: entryData, entryDigest: entryDigest}
}

func writeSecureFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	secureOwnerOnlyForTest(t, path)
}

var _ = model.RuntimeBinding{}

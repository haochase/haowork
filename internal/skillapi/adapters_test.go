package skillapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/patchskills"
	"github.com/haochase/haowork/internal/skillruntime"
)

func TestCoreAdaptersCallExistingServiceWithoutDirectEventWrites(t *testing.T) {
	store := &adapterStore{}
	service := app.New("PRJ-001", 1, store, nil, nil)
	adapter := HistoryAdapter{Service: service}

	output, _, err := adapter.Invoke(context.Background(), skillruntime.Invocation{TaskID: "TSK-001"})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if store.appended != 0 || !json.Valid(output) {
		t.Fatalf("direct writes/output = %d / %s", store.appended, output)
	}
}

func TestSignedCrossZoneAdaptersOnlyExposeConfiguredSkillNames(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	adapters := SignedCrossZoneAdapters(patchskills.NewEd25519Signer("key-1", private), "AGT-BUILD", "AGT-VERIFY")
	for _, name := range []string{"advisory", "mirror", "patch", "audit"} {
		if _, ok := adapters[name].(patchskills.Adapter); !ok {
			t.Fatalf("%s adapter = %T, want signed cross-zone adapter", name, adapters[name])
		}
	}
	output, _, err := adapters.Invoke(context.Background(), skillruntime.Invocation{MissionID: "MSN-1", EnvironmentID: "internal", SkillName: "patch", SkillVersion: "1.0.0", LogicalActorID: "AGT-BUILD", RuntimeBindingRevision: 1, Scope: []string{"src/**"}, Input: json.RawMessage(`{"paths":["src/main.go"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	var request patchskills.SignedRequest
	if err := json.Unmarshal(output, &request); err != nil || request.Request.BuildAgentID != "AGT-BUILD" || request.Request.VerifyAgentID != "AGT-VERIFY" {
		t.Fatalf("configured patch adapter output = %s, %v", output, err)
	}
}

func TestCoreAdaptersWithCrossZoneInvokesConfiguredAuditWithAuditFacts(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := app.New("PRJ-001", 1, &adapterStore{}, nil, nil)
	adapters := CoreAdaptersWithCrossZone(service, CrossZoneConfig{Signer: patchskills.NewEd25519Signer("key-1", private), BuildAgentID: "AGT-BUILD", VerifyAgentID: "AGT-VERIFY", AuditCommand: "go test ./...", AuditExitCode: 0, WorkspaceDigest: "workspace-sha", ArtifactHash: "artifact-sha"})
	output, _, err := adapters.Invoke(context.Background(), skillruntime.Invocation{MissionID: "MSN-1", EnvironmentID: "internal", SkillName: "audit", SkillVersion: "1.0.0", LogicalActorID: "AGT-VERIFY", RuntimeBindingRevision: 1, Scope: []string{"src/**"}, Input: json.RawMessage(`{"run":"audit"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var signed patchskills.SignedRequest
	if err := json.Unmarshal(output, &signed); err != nil {
		t.Fatal(err)
	}
	if signed.Request.Audit.Command != "go test ./..." || signed.Request.Audit.WorkspaceDigest != "workspace-sha" || signed.Request.Audit.ArtifactHash != "artifact-sha" {
		t.Fatalf("audit facts = %#v", signed.Request.Audit)
	}
}

func TestCoreAdaptersFailClosedWithoutRealServiceContract(t *testing.T) {
	service := app.New("PRJ-001", 1, &adapterStore{}, nil, nil)
	adapters := CoreAdapters(service)
	for _, name := range []string{"plan", "record", "verify"} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := adapters[name].Invoke(context.Background(), skillruntime.Invocation{}); err == nil {
				t.Fatalf("%s succeeded without a concrete Service contract", name)
			}
		})
	}
}

type adapterStore struct{ appended int }

func (store *adapterStore) Append(context.Context, model.Event) (model.Event, error) {
	store.appended++
	return model.Event{}, nil
}

func (store *adapterStore) AppendIfUnchanged(context.Context, model.Event, int) (model.Event, error) {
	store.appended++
	return model.Event{}, nil
}

func (*adapterStore) ReadAll(context.Context) ([]model.Event, error) { return nil, nil }

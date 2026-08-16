package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/evidence"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestVerifyContextualEvidenceMapsRealLocalCoreGate(t *testing.T) {
	for _, decision := range []string{"rejected", "stale"} {
		t.Run(decision, func(t *testing.T) {
			root := initializeLocalAPIFallbackProject(t)
			if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("brief"), 0o600); err != nil {
				t.Fatal(err)
			}
			project, err := openProject(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
			req, tasks, err := project.Service.Plan(context.Background(), app.PlanInput{Title: "Evidence", Tasks: []app.TaskInput{{Title: "Verify", AcceptanceCriteria: []string{"gate"}}}, Actor: owner})
			if err != nil {
				t.Fatal(err)
			}
			if err = project.Service.Approve(context.Background(), req.ID, owner); err != nil {
				t.Fatal(err)
			}
			slice, err := project.Service.BuildContext(context.Background(), app.ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Actor: owner})
			if err != nil {
				t.Fatal(err)
			}
			appendCLIContextRun(t, project, "run.started", model.RunStarted{Run: model.Run{ID: "RUN-CLI", TaskID: tasks[0].ID, GoalVersion: 1, Executor: "test", ActorID: "AGT-1", ContextID: slice.ID, ContextHash: slice.SliceHash}}, owner)
			appendCLIContextRun(t, project, "run.finished", model.RunFinished{RunID: "RUN-CLI", Result: "done"}, owner)
			project.Service.ConfigureEvidenceVerifier(cliDecisionVerifier{status: decision})
			key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			server := &localapi.Server{Project: project, ControlKey: key, Changes: cliStaticScanner{}}
			defer server.Close()
			api := httptest.NewServer(server.Handler())
			defer api.Close()
			metadata := localcore.Metadata{ProjectID: "PRJ-CLI", Endpoint: api.URL, PID: 4242, StartedAt: time.Now().UTC(), ControlKey: key}
			server.Metadata = metadata
			writeLocalCoreMetadata(t, root, metadata)
			path := filepath.Join(t.TempDir(), "evidence.log")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), []string{"verify", tasks[0].ID, "--project", root, "--kind", "test", "--evidence", path, "--outcome", "pass", "--run-id", "RUN-CLI", "--context-id", slice.ID, "--command", "ignored", "--actor", "USR-REVIEWER", "--role", "reviewer", "--json"}, &stdout, &stderr)
			if code != ExitGate {
				t.Fatalf("code = %d, want ExitGate; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			state, err := project.Service.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Evidence[tasks[0].ID]) != 1 || state.Evidence[tasks[0].ID][0].Status != "invalidated" {
				t.Fatalf("evidence = %#v", state.Evidence[tasks[0].ID])
			}
		})
	}
}

func appendCLIContextRun(t *testing.T, project core.Project, kind string, payload any, actor model.Actor) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = project.Events.Append(context.Background(), model.Event{ID: "EVT-" + kind, Type: kind, ProjectID: "PRJ-CLI", GoalVersion: 1, AggregateType: "run", AggregateID: "RUN-CLI", Actor: actor, OccurredAt: time.Now().UTC(), Payload: encoded}); err != nil {
		t.Fatal(err)
	}
}

type cliDecisionVerifier struct{ status string }

func (v cliDecisionVerifier) Verify(context.Context, evidence.EvidenceCandidate) (evidence.EvidenceDecision, error) {
	return evidence.EvidenceDecision{Status: v.status}, nil
}

type cliStaticScanner struct{}

func (cliStaticScanner) Scan(context.Context, string) ([]model.FileChange, error) { return nil, nil }

func TestPlanFallsBackWhenLocalCoreIdentityDoesNotMatchProject(t *testing.T) {
	tests := []struct {
		name              string
		metadataProjectID string
		healthProjectID   string
	}{
		{
			name:              "core metadata project mismatch",
			metadataProjectID: "PRJ-OTHER",
			healthProjectID:   "PRJ-OTHER",
		},
		{
			name:              "health project mismatch",
			metadataProjectID: "PRJ-CLI",
			healthProjectID:   "PRJ-OTHER",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := initializeLocalAPIFallbackProject(t)
			var controlHeader string
			wrongCore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/_haowork/health":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(struct {
						ProjectID       string `json:"project_id"`
						ProtocolVersion string `json:"protocol_version"`
						PID             int    `json:"pid"`
					}{ProjectID: test.healthProjectID, ProtocolVersion: "v1", PID: 4242})
				case "/api/v1/requirements":
					controlHeader = r.Header.Get("X-Haowork-Control")
					w.WriteHeader(http.StatusInternalServerError)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer wrongCore.Close()
			writeLocalCoreMetadata(t, root, localcore.Metadata{
				ProjectID:  test.metadataProjectID,
				Endpoint:   wrongCore.URL,
				PID:        4242,
				StartedAt:  time.Now().UTC(),
				ControlKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
			})

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Execute(context.Background(), []string{
				"plan", "create",
				"--project", root,
				"--title", "Fallback to direct service",
				"--task", "Avoid wrong Core",
				"--acceptance", "does not send control credential",
				"--actor", "USR-OWNER",
				"--role", "owner",
				"--json",
			}, &stdout, &stderr)

			if controlHeader != "" {
				t.Fatalf("sent X-Haowork-Control to mismatched Core: %q", controlHeader)
			}
			if code != ExitOK {
				t.Fatalf("plan create code = %d, want P0-01 direct fallback success; stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestStartOrReuseLocalCoreSerializesConcurrentSpawns(t *testing.T) {
	root := initializeLocalAPIFallbackProject(t)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_haowork/health" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			ProjectID       string `json:"project_id"`
			ProtocolVersion string `json:"protocol_version"`
			PID             int    `json:"pid"`
		}{ProjectID: "PRJ-CLI", ProtocolVersion: "v1", PID: 4242})
	}))
	defer core.Close()

	firstSpawnEntered := make(chan struct{})
	releaseFirstSpawn := make(chan struct{})
	unexpectedSpawn := make(chan struct{}, 1)
	var spawns atomic.Int32
	spawn := func(projectRoot string) error {
		call := spawns.Add(1)
		if call == 1 {
			close(firstSpawnEntered)
			<-releaseFirstSpawn
		} else {
			unexpectedSpawn <- struct{}{}
		}
		return writeLocalCoreMetadataFile(projectRoot, localcore.Metadata{
			ProjectID:  "PRJ-CLI",
			Endpoint:   core.URL,
			PID:        4242,
			StartedAt:  time.Now().UTC(),
			ControlKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		})
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := startOrReuseLocalCoreWithSpawner(context.Background(), root, "PRJ-CLI", spawn)
		firstDone <- err
	}()
	select {
	case <-firstSpawnEntered:
	case <-time.After(time.Second):
		t.Fatal("first open did not start the local Core")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := startOrReuseLocalCoreWithSpawner(context.Background(), root, "PRJ-CLI", spawn)
		secondDone <- err
	}()
	select {
	case <-unexpectedSpawn:
		t.Fatal("concurrent open started a second local Core before metadata became healthy")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirstSpawn)

	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("startOrReuseLocalCore() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent open did not finish")
		}
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("local Core spawn count = %d, want 1", got)
	}
}

func initializeLocalAPIFallbackProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if _, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root:               root,
		Name:               "CLI fallback test",
		ProjectID:          "PRJ-CLI",
		Goal:               "Keep commands project-bound",
		CompletionCriteria: []string{"direct fallback succeeds"},
		Actor:              model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("initialize project: %v", err)
	}
	return root
}

func writeLocalCoreMetadata(t *testing.T, root string, metadata localcore.Metadata) {
	t.Helper()
	if err := writeLocalCoreMetadataFile(root, metadata); err != nil {
		t.Fatalf("write core metadata: %v", err)
	}
}

func writeLocalCoreMetadataFile(root string, metadata localcore.Metadata) error {
	directory := filepath.Join(root, ".haowork", "runtime")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "core.json"), data, 0o600); err != nil {
		return err
	}
	return nil
}

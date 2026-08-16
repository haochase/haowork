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
	"reflect"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/localapi"
	"github.com/haochase/haowork/internal/localcore"
	"github.com/haochase/haowork/internal/model"
)

func TestContextCLIShowMatchesHealthyLocalAPIJSON(t *testing.T) {
	root := initializeLocalAPIFallbackProject(t)
	if err := os.WriteFile(filepath.Join(root, "brief.txt"), []byte("context"), 0o600); err != nil {
		t.Fatal(err)
	}
	project, err := openProject(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	owner := model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}
	requirement, tasks, err := project.Service.Plan(context.Background(), app.PlanInput{Title: "Build context", Tasks: []app.TaskInput{{Title: "Prepare", AcceptanceCriteria: []string{"context available"}}}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Service.Approve(context.Background(), requirement.ID, owner); err != nil {
		t.Fatal(err)
	}
	slice, err := project.Service.BuildContext(context.Background(), app.ContextBuildInput{TaskID: tasks[0].ID, Sources: []string{"brief.txt"}, Actor: owner})
	if err != nil {
		t.Fatal(err)
	}
	controlKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	server := &localapi.Server{Project: project, ControlKey: controlKey}
	api := httptest.NewServer(server.Handler())
	defer api.Close()
	metadata := localcore.Metadata{ProjectID: "PRJ-CLI", Endpoint: api.URL, PID: 4242, StartedAt: time.Now().UTC(), ControlKey: controlKey}
	server.Metadata = metadata
	writeLocalCoreMetadata(t, root, metadata)

	request, err := http.NewRequest(http.MethodGet, api.URL+"/api/v1/context/"+slice.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Haowork-Control", metadata.ControlKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("API get status = %d", response.StatusCode)
	}
	var apiJSON map[string]any
	if err := json.NewDecoder(response.Body).Decode(&apiJSON); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute(context.Background(), []string{"context", "show", slice.ID, "--project", root, "--json"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("context show code = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var cliJSON map[string]any
	if err := json.NewDecoder(&stdout).Decode(&cliJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(apiJSON, cliJSON) {
		t.Fatalf("API and CLI context JSON differ: api=%#v cli=%#v", apiJSON, cliJSON)
	}
}

func TestContextBuildMapsLocalAPIStatusToCLIExitCode(t *testing.T) {
	for _, test := range []struct {
		status int
		want   int
	}{
		{status: http.StatusForbidden, want: ExitApproval},
		{status: http.StatusConflict, want: ExitConflict},
		{status: http.StatusUnprocessableEntity, want: ExitGate},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			root := initializeLocalAPIFallbackProject(t)
			controlKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/_haowork/health":
					_ = json.NewEncoder(w).Encode(map[string]any{"project_id": "PRJ-CLI", "protocol_version": "v1", "pid": 4242})
				case "/api/v1/tasks/TSK-001/context":
					w.WriteHeader(test.status)
					_ = json.NewEncoder(w).Encode(map[string]string{"code": "test", "message": "mapped"})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			writeLocalCoreMetadata(t, root, localcore.Metadata{ProjectID: "PRJ-CLI", Endpoint: api.URL, PID: 4242, StartedAt: time.Now().UTC(), ControlKey: controlKey})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Execute(context.Background(), []string{"context", "build", "TSK-001", "--project", root, "--source", "brief.txt", "--actor", "USR-OWNER", "--role", "owner", "--json"}, &stdout, &stderr)
			if code != test.want {
				t.Fatalf("context build status %d exit = %d, want %d; stdout=%q stderr=%q", test.status, code, test.want, stdout.String(), stderr.String())
			}
		})
	}
}

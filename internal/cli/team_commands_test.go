package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
)

func TestTeamCommandTreeExposesDeterministicLifecycleCommands(t *testing.T) {
	for _, args := range [][]string{{"team", "--help"}, {"team", "lease", "--help"}, {"team", "goal", "--help"}, {"team", "conflict", "--help"}} {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(context.Background(), args, &stdout, &stderr)
		if code != cli.ExitOK {
			t.Fatalf("%v code = %d, stderr = %q", args, code, stderr.String())
		}
		if strings.TrimSpace(stdout.String()) == "" {
			t.Fatalf("%v help is empty", args)
		}
	}
}

func TestTeamJoinImportsRemoteAcceptedHistoryBeforeCoreOpen(t *testing.T) {
	ctx := context.Background()
	teamRoot := t.TempDir()
	localRoot := t.TempDir()
	clock := fixedClock{value: time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)}
	input := app.InitializeProjectInput{Root: teamRoot, Name: "join", ProjectID: "PRJ-JOIN", Goal: "preserve history", CompletionCriteria: []string{"history readable"}, Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner}}
	if _, err := app.InitializeProject(ctx, input, idgen.New(), clock); err != nil {
		t.Fatal(err)
	}
	input.Root = localRoot
	if _, err := app.InitializeProject(ctx, input, idgen.New(), clock); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.yaml", "events.jsonl"} {
		data, err := os.ReadFile(filepath.Join(teamRoot, ".haowork", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(localRoot, ".haowork", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service, err := team.New(ctx, teamRoot, team.Dependencies{IDs: idgen.New(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	principal := team.Principal{AuthenticatedPrincipal: "USR-OWNER", Actor: input.Actor, DeviceID: "DEV-JOIN", EnvironmentID: "ENV-JOIN"}
	digest, err := teamapi.TokenSHA256(token)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := teamapi.LoadStaticAuthenticator(writeAuthFile(t, digest, principal))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&teamapi.Server{ProjectID: "PRJ-JOIN", Service: service, Authenticator: auth}).Handler())
	defer server.Close()
	t.Setenv("HAOWORK_TEAM_TOKEN", token)
	var stdout, stderr bytes.Buffer
	code := cli.Execute(ctx, []string{"--project", localRoot, "--json", "team", "join", "--endpoint", server.URL, "--device-id", "DEV-JOIN", "--environment-id", "ENV-JOIN"}, &stdout, &stderr)
	if code != cli.ExitOK {
		t.Fatalf("team join code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	accepted := eventstore.NewAt(filepath.Join(localRoot, ".haowork", "team", "events.jsonl"), filepath.Join(localRoot, ".haowork", "team", "events.lock"))
	history, err := accepted.ReadAll(ctx)
	if err != nil || len(history) == 0 {
		t.Fatalf("joined accepted history=%#v err=%v", history, err)
	}
	project, err := core.Open(ctx, localRoot, core.Dependencies{IDs: idgen.New(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	projectHistory, err := project.Events.ReadAll(ctx)
	if err != nil || len(projectHistory) != len(history) {
		t.Fatalf("joined project history=%d accepted=%d err=%v", len(projectHistory), len(history), err)
	}
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func writeAuthFile(t *testing.T, digest string, principal team.Principal) string {
	t.Helper()
	payload, err := json.Marshal(teamapi.StaticAuthFile{Credentials: []teamapi.StaticCredential{{TokenSHA256: digest, Principal: principal}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

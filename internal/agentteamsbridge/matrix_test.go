package agentteamsbridge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestMatrixV3LoginAndSendUseOfficialEndpoints(t *testing.T) {
	t.Helper()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/_matrix/client/v3/login":
			var body struct {
				Type       string `json:"type"`
				Identifier struct {
					Type string `json:"type"`
					User string `json:"user"`
				} `json:"identifier"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Type != "m.login.password" || body.Identifier.Type != "m.id.user" || body.Identifier.User != "manager" || body.Password != "matrix-password" {
				t.Fatalf("login body = %#v", body)
			}
			_, _ = response.Write([]byte(`{"access_token":"matrix-token","user_id":"@manager:matrix.test"}`))
		case request.Method == http.MethodGet && request.URL.Path == "/_matrix/client/v3/joined_rooms":
			if request.Header.Get("Authorization") != "Bearer matrix-token" {
				t.Fatalf("joined_rooms Authorization = %q", request.Header.Get("Authorization"))
			}
			_, _ = response.Write([]byte(`{"joined_rooms":["!leader:matrix.test"]}`))
		case request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/_matrix/client/v3/rooms/!leader:matrix.test/send/m.room.message/"):
			if request.Header.Get("Authorization") != "Bearer matrix-token" {
				t.Fatalf("send Authorization = %q", request.Header.Get("Authorization"))
			}
			if strings.TrimPrefix(request.URL.Path, "/_matrix/client/v3/rooms/!leader:matrix.test/send/m.room.message/") == "" {
				t.Fatal("Matrix send omitted stable transaction ID")
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			artifacts, ok := body["org.haowork.artifacts"].([]any)
			if !ok || len(artifacts) != 1 {
				t.Fatalf("send artifact evidence = %#v", body["org.haowork.artifacts"])
			}
			artifact, ok := artifacts[0].(map[string]any)
			if !ok || artifact["uri"] != "environments/public/missions/MSN-001/artifacts/abc" || artifact["sha256"] != strings.Repeat("a", 64) || artifact["environmentID"] != "public" || artifact["size"] != float64(42) {
				t.Fatalf("send artifact evidence = %#v", artifact)
			}
			if body["msgtype"] != "m.text" || body["body"] != "Haowork governed mission assigned. Read the attached mission reference and reply with a concise completion summary." || body["org.haowork.mission_id"] != "MSN-001" || body["org.haowork.artifact_ref"] != "environments/public/missions/MSN-001/artifacts/abc" {
				t.Fatalf("send body = %#v", body)
			}
			_, _ = response.Write([]byte(`{"event_id":"$event-001"}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{
		BaseURL: server.URL, Username: "manager", Password: "matrix-password", Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	rooms, err := client.JoinedRooms(context.Background())
	if err != nil || len(rooms) != 1 || rooms[0] != "!leader:matrix.test" {
		t.Fatalf("joined rooms = %#v, err=%v", rooms, err)
	}
	if err := client.Send(context.Background(), "!leader:matrix.test", agentteamsbridge.MatrixOutbound{
		MissionID: "MSN-001", RunID: "RUN-001", WorkItemID: "WKI-001", WorkspaceDigest: strings.Repeat("b", 64), ArtifactRef: "environments/public/missions/MSN-001/artifacts/abc",
		Artifact: agentteamsbridge.MatrixArtifact{URI: "environments/public/missions/MSN-001/artifacts/abc", SHA256: strings.Repeat("a", 64), EnvironmentID: "public", Size: 42},
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("official Matrix requests = %#v", requests)
	}
}

func TestMatrixMessagesPreserveOpaquePaginationToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/_matrix/client/v3/rooms/!leader:matrix.test/messages" {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if request.URL.Query().Get("dir") != "f" || request.URL.Query().Get("from") != "opaque-token/with?characters" {
			t.Fatalf("messages query = %q", request.URL.RawQuery)
		}
		_, _ = response.Write([]byte(`{
  "start":"opaque-token/with?characters",
  "end":"opaque-next-token",
  "chunk":[{
    "type":"m.room.message",
    "event_id":"$event-001",
    "sender":"@worker-build:matrix.test",
    "content":{"msgtype":"m.text","body":"do not persist this Matrix message","org.haowork.workspace_digest":"workspace-sha","org.haowork.mission_id":"MSN-001","org.haowork.run_id":"RUN-001","org.haowork.work_item_id":"WKI-001","org.haowork.artifacts":[{"uri":"environments/public/missions/MSN-001/artifacts/abc","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environmentID":"public","size":42}]}
  }]
}`))
	}))
	defer server.Close()

	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "matrix-token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Messages(context.Background(), "!leader:matrix.test", "opaque-token/with?characters")
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "opaque-next-token" || !page.More || len(page.Events) != 1 {
		t.Fatalf("page = %#v", page)
	}
	if page.Events[0].Summary != "" || page.Events[0].SummarySHA256 == "" || page.Events[0].WorkspaceDigest != "workspace-sha" || page.Events[0].MissionID != "MSN-001" || page.Events[0].RunID != "RUN-001" || page.Events[0].WorkItemID != "WKI-001" || len(page.Events[0].Artifacts) != 1 || page.Events[0].Artifacts[0].SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("Matrix message body leaked into event = %#v", page.Events[0])
	}
	digest := sha256.Sum256([]byte("do not persist this Matrix message"))
	if page.Events[0].SummarySHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("summary digest = %q", page.Events[0].SummarySHA256)
	}
}

func TestMatrixEventIdentityComesFromSenderAndEventID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_matrix/client/v3/rooms/!leader:matrix.test/messages":
			_, _ = response.Write([]byte(`{"chunk":[{"type":"m.room.message","event_id":"$source-event","sender":"@worker-verify:matrix.test","content":{"msgtype":"m.notice","body":"result"}}]}`))
		case "/_matrix/client/v3/rooms/!leader:matrix.test/members":
			_, _ = response.Write([]byte(`{"chunk":[{"type":"m.room.member","state_key":"@worker-verify:matrix.test","content":{"membership":"join"}}]}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "matrix-token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Messages(context.Background(), "!leader:matrix.test", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != "$source-event" || page.Events[0].SenderID != "@worker-verify:matrix.test" || page.Events[0].RoomID != "!leader:matrix.test" {
		t.Fatalf("event identity = %#v", page.Events)
	}
	members, err := client.Members(context.Background(), "!leader:matrix.test")
	if err != nil || len(members) != 1 || members[0].UserID != "@worker-verify:matrix.test" || members[0].Membership != "join" {
		t.Fatalf("members = %#v, err=%v", members, err)
	}
}

func TestMatrixRejectsTrailingJSONWrongRoomAndOversizedBody(t *testing.T) {
	for name, body := range map[string]string{
		"trailing":   `{"chunk":[]}{"chunk":[]}`,
		"wrong room": `{"chunk":[{"type":"m.room.message","event_id":"$event","sender":"@worker:test","room_id":"!other:test","content":{"msgtype":"m.notice","body":"result"}}]}`,
		"oversized":  `{"chunk":[{"type":"m.room.message","event_id":"$event","sender":"@worker:test","content":{"body":"` + strings.Repeat("x", 512) + `"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_, _ = response.Write([]byte(body))
			}))
			defer server.Close()
			client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "matrix-token", Client: server.Client(), MaxBodyBytes: 256})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Messages(context.Background(), "!leader:test", ""); err == nil {
				t.Fatal("Messages accepted malformed Matrix response")
			}
		})
	}
}

func TestMatrixAcceptsOfficialExtensionsButRejectsMalformedFields(t *testing.T) {
	for name, body := range map[string]string{
		"official extensions": `{"chunk":[{"type":"m.room.message","event_id":"$event","sender":"@worker:test","origin_server_ts":1720000000000,"unsigned":{"age":10},"content":{"msgtype":"m.text","body":"result","m.relates_to":{"rel_type":"m.thread"}}}],"end":"opaque-next","unstable_extension":{"enabled":true}}`,
		"malicious type":      `{"chunk":[{"type":"m.room.message","event_id":7,"sender":"@worker:test","content":{"body":"result"}}]}`,
		"multiple documents":  `{"chunk":[]}{"chunk":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_, _ = response.Write([]byte(body))
			}))
			defer server.Close()
			client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "matrix-token", Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			page, err := client.Messages(context.Background(), "!leader:test", "")
			if name == "official extensions" {
				if err != nil || len(page.Events) != 1 || page.NextCursor != "opaque-next" {
					t.Fatalf("official Matrix extensions result = %#v, err=%v", page, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Matrix accepted malformed %s response", name)
			}
		})
	}
}

func TestMatrixWriteDoesNotFollowRedirectOrRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		response.Header().Set("Location", "/redirect-target")
		response.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "matrix-token", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "!leader:test", agentteamsbridge.MatrixOutbound{MissionID: "MSN-001", RunID: "RUN-001", WorkItemID: "WKI-001", WorkspaceDigest: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("Matrix Send followed a redirect")
	}
	if calls != 1 {
		t.Fatalf("Matrix Send calls = %d, want one", calls)
	}
}

func TestMatrixRejectsControlPlaneURLWithUserinfo(t *testing.T) {
	_, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{
		BaseURL: "https://operator:password@matrix.example.test", AccessToken: "matrix-token",
	})
	if err == nil {
		t.Fatal("Matrix client accepted control-plane URL userinfo")
	}
}

func TestMatrixAllowsOnlyExplicitClusterLocalHTTP(t *testing.T) {
	if _, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{
		BaseURL: "http://agentteams-tuwunel.haowork-public.svc.cluster.local:8008", AccessToken: "token", AllowInsecureClusterLocal: true,
	}); err != nil {
		t.Fatalf("cluster-local Matrix URL rejected: %v", err)
	}
	if _, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{
		BaseURL: "http://matrix.example.test", AccessToken: "token", AllowInsecureClusterLocal: true,
	}); err == nil {
		t.Fatal("arbitrary insecure Matrix URL was accepted")
	}
}

func TestMatrixV3SelectsObservedLeaderRoomAndCarriesWorkspaceDigest(t *testing.T) {
	var paths []string
	var content map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPut {
			if err := json.NewDecoder(request.Body).Decode(&content); err != nil {
				t.Fatalf("decode Matrix send: %v", err)
			}
			_, _ = response.Write([]byte(`{"event_id":"$delegated"}`))
			return
		}
		_, _ = response.Write([]byte(`{"next_batch":"s1","rooms":{"join":{"!leader:example":{"timeline":{"events":[]}}}}}`))
	}))
	defer server.Close()

	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{
		BaseURL: server.URL, AccessToken: "matrix-token", DefaultRoomID: "!stale:example", Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SelectRoom("!leader:example"); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	if err := client.Send(context.Background(), "!leader:example", agentteamsbridge.MatrixOutbound{
		MissionID: "MSN-1", RunID: "RUN-1", WorkItemID: "WORK-1", WorkspaceDigest: digest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Sync(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "!leader:example") || paths[1] != "/_matrix/client/v3/sync" {
		t.Fatalf("Matrix paths = %#v, want observed leader room for send and official sync", paths)
	}
	if content["org.haowork.workspace_digest"] != digest {
		t.Fatalf("workspace digest = %#v", content["org.haowork.workspace_digest"])
	}
}

func TestMatrixV3MapsOfficialNoticeWithoutTrustingRoleClaims(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"next_batch":"s1","rooms":{"join":{"!leader:example":{"timeline":{"events":[{"type":"m.room.message","event_id":"$notice","sender":"@manager:example","room_id":"!leader:example","content":{"msgtype":"m.notice","body":"delegated","org.haowork.workspace_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","org.haowork.mission_id":"MSN-1","org.haowork.run_id":"RUN-1","org.haowork.work_item_id":"WORK-1"}}]}}}}}`))
	}))
	defer server.Close()
	client, err := agentteamsbridge.NewMatrixV3Client(agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AccessToken: "token", DefaultRoomID: "!leader:example", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Sync(context.Background(), "s0")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Kind != "notice" {
		t.Fatalf("official Matrix events = %#v", page.Events)
	}
	if page.Events[0].SenderRole != "" || page.Events[0].AgentFunction != "" {
		t.Fatalf("Matrix adapter trusted role claims: %#v", page.Events[0])
	}
}

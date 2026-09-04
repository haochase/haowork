package agentteamsbridge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestKubernetesHumanMatrixClientLogsInAsBoundHuman(t *testing.T) {
	var loginSeen, sendSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_matrix/client/v3/login":
			if request.Header.Get("Authorization") != "Bearer test-as-token" {
				t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			identifier, _ := body["identifier"].(map[string]any)
			if identifier["user"] != "mission-owner" || body["type"] != "m.login.application_service" {
				t.Fatalf("login body = %#v", body)
			}
			if _, exists := body["password"]; exists {
				t.Fatalf("AppService login leaked a password field: %#v", body)
			}
			loginSeen = true
			_, _ = response.Write([]byte(`{"access_token":"human-token","user_id":"@mission-owner:matrix.test"}`))
		case strings.Contains(request.URL.Path, "/send/m.room.message/"):
			if request.Header.Get("Authorization") != "Bearer human-token" {
				t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
			}
			sendSeen = true
			_, _ = response.Write([]byte(`{"event_id":"$delegated"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	human := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agentteams.io/v1beta1", "kind": "Human",
		"metadata": map[string]any{"name": "mission-owner", "namespace": "haowork-public", "uid": "human-uid"},
		"spec":     map[string]any{"username": "mission-owner"},
		"status":   map[string]any{"phase": "Active", "matrixUserID": "@mission-owner:matrix.test"},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), human)
	client, err := agentteamsbridge.NewKubernetesHumanMatrixClient(dynamicClient, "haowork-public", agentteamsbridge.MatrixV3Config{BaseURL: server.URL, AppServiceToken: "test-as-token"})
	if err != nil {
		t.Fatal(err)
	}
	identity := agentteamsbridge.HumanMatrixIdentity{Name: "mission-owner", UID: "human-uid", PrincipalID: "@mission-owner:matrix.test"}
	if err := client.BindHuman(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), "!manager:matrix.test", agentteamsbridge.MatrixOutbound{MissionID: "MSN-1", RunID: "RUN-1", WorkItemID: "WORK-1", WorkspaceDigest: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if !loginSeen || !sendSeen {
		t.Fatalf("login=%t send=%t", loginSeen, sendSeen)
	}
	if err := client.BindHuman(context.Background(), agentteamsbridge.HumanMatrixIdentity{Name: "mission-owner", UID: "replacement-uid", PrincipalID: "@mission-owner:matrix.test"}); err == nil {
		t.Fatal("same-name replacement Human was accepted")
	}
}

func TestKubernetesHumanMatrixClientRejectsMissingAppServiceToken(t *testing.T) {
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if _, err := agentteamsbridge.NewKubernetesHumanMatrixClient(dynamicClient, "haowork-public", agentteamsbridge.MatrixV3Config{BaseURL: "https://matrix.test"}); err == nil {
		t.Fatal("Kubernetes Human Matrix client accepted missing AppService token")
	}
}

func TestKubernetesHumanMatrixClientRejectsDeletingHuman(t *testing.T) {
	deletingAt := metav1.NewTime(time.Now())
	human := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agentteams.io/v1beta1", "kind": "Human",
		"metadata": map[string]any{"name": "mission-owner", "namespace": "haowork-public", "uid": "human-uid", "finalizers": []any{"agentteams.io/human-finalizer"}},
		"spec":     map[string]any{"username": "mission-owner"},
		"status":   map[string]any{"phase": "Active", "matrixUserID": "@mission-owner:matrix.test"},
	}}
	human.SetDeletionTimestamp(&deletingAt)
	dynamicClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), human)
	client, err := agentteamsbridge.NewKubernetesHumanMatrixClient(dynamicClient, "haowork-public", agentteamsbridge.MatrixV3Config{BaseURL: "https://matrix.test", AppServiceToken: "test-as-token"})
	if err != nil {
		t.Fatal(err)
	}
	identity := agentteamsbridge.HumanMatrixIdentity{Name: "mission-owner", UID: "human-uid", PrincipalID: "@mission-owner:matrix.test"}
	if err := client.BindHuman(context.Background(), identity); err == nil {
		t.Fatal("deleting Human was accepted for Matrix delegation")
	}
}

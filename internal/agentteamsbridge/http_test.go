package agentteamsbridge_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestHTTPMatrixClientRejectsTrailingJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nextCursor":"one"}{"nextCursor":"two"}`))
	}))
	defer server.Close()
	if _, err := agentteamsbridge.NewHTTPMatrixClient(server.URL, server.Client(), "").Sync(context.Background(), "saved"); err == nil {
		t.Fatal("Sync accepted trailing JSON")
	}
}

func TestHTTPMatrixClientUsesStrictLoopbackRequestsForSendSyncAndStop(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/matrix/sync":
			_, _ = w.Write([]byte(`{"nextCursor":"next","events":[{"id":"$one","kind":"notice","workspaceDigest":"workspace-1","senderID":"runtime-1","senderRole":"agent"}]}`))
		case "/api/v1/matrix/rooms/!room/send", "/api/v1/matrix/rooms/!room/stop":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := agentteamsbridge.NewHTTPMatrixClient(server.URL, server.Client(), "")
	if err := client.Send(context.Background(), "!room", agentteamsbridge.MatrixOutbound{MissionID: "MSN-001"}); err != nil {
		t.Fatal(err)
	}
	page, err := client.Sync(context.Background(), "saved")
	if err != nil || page.NextCursor != "next" || len(page.Events) != 1 {
		t.Fatalf("sync = %#v, err=%v", page, err)
	}
	if err := client.Stop(context.Background(), "!room"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("requests = %#v", paths)
	}
}

func TestHTTPArtifactStoreRejectsRedirectAndDownloadsVerifiedRelativeArtifact(t *testing.T) {
	data := []byte("artifact")
	digest := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifacts/evidence/report" {
			_, _ = w.Write(data)
			return
		}
		w.Header().Set("Location", "/artifacts/evidence/report")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	store := agentteamsbridge.NewHTTPArtifactStore(server.URL+"/artifacts", server.Client(), "")
	got, err := agentteamsbridge.VerifyArtifactDownload(context.Background(), store, "evidence/report", hex.EncodeToString(digest[:]))
	if err != nil || string(got) != string(data) {
		t.Fatalf("download = %q, err=%v", got, err)
	}
	if _, err := store.Download(context.Background(), "../redirect"); err == nil {
		t.Fatal("relative path escape accepted")
	}
}

func TestHTTPArtifactStoreRefusesMismatchedUploadDigestBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	store := agentteamsbridge.NewHTTPArtifactStore(server.URL, server.Client(), "")
	if _, err := store.Upload(context.Background(), "missions/MSN-001.json", []byte("data"), "not-a-sha"); err == nil {
		t.Fatal("Upload accepted malformed digest")
	}
	if _, err := store.Upload(context.Background(), "missions/MSN-001.json", []byte("data"), strings.Repeat("0", 64)); err == nil {
		t.Fatal("Upload accepted mismatched digest")
	}
	if calls != 0 {
		t.Fatalf("upload sent %d invalid requests", calls)
	}
}

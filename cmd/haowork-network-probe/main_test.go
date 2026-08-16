package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunProbeRejectsReachableHTTPService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	err := run(context.Background(), []string{"probe", server.URL}, server.Client(), time.Second)
	if err == nil {
		t.Fatal("reachable HTTP service was accepted")
	}
}

func TestProbeManifestsAreIsolatedAndUseOnlyLocalImage(t *testing.T) {
	root := filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2")
	for _, zone := range []string{"public", "internal"} {
		content, err := os.ReadFile(filepath.Join(root, "haowork-network-probe-"+zone+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, required := range []string{
			"kind: Deployment", "name: haowork-network-probe", "namespace: haowork-" + zone,
			"image: haowork-network-probe:local", "imagePullPolicy: Never", "runAsNonRoot: true",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s probe manifest omits %q", zone, required)
			}
		}
		for _, forbidden := range []string{"kind: Service", "kind: Secret", "hostNetwork: true", "type: LoadBalancer"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s probe manifest contains forbidden %q", zone, forbidden)
			}
		}
	}
}

func TestRunProbePropagatesNetworkTimeout(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	err := run(context.Background(), []string{"probe", "http://matrix.example.test"}, client, time.Second)
	if err == nil || err.Error() == "" {
		t.Fatal("network failure was not returned")
	}
}

func TestRunServeStopsWithContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, []string{"serve"}, http.DefaultClient, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("serve err=%v, want context cancellation", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

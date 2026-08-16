package teamapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/team"
)

func TestNewClientRejectsNonLoopbackPlainHTTP(t *testing.T) {
	if _, err := NewClient("http://example.com", staticTokenSource{token: testToken(1)}, nil); err == nil {
		t.Fatal("NewClient accepted non-loopback plain HTTP")
	}
}

func TestClientPushSendsExactlyOnceWithoutRetry(t *testing.T) {
	transport := &countingTransport{status: http.StatusServiceUnavailable, body: `{"code":"unavailable","message":"try again"}`}
	client, err := NewClient("http://127.0.0.1:8080", staticTokenSource{token: testToken(1)}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Push(context.Background(), team.PushBatch{BatchID: "BATCH-1"})
	if err == nil {
		t.Fatal("Push succeeded despite server failure")
	}
	if transport.calls != 1 {
		t.Fatalf("request calls = %d, want exactly 1", transport.calls)
	}
}

func TestClientPushRejectsRedirectWithoutResending(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &redirectTransport{status: status}
			client, err := NewClient("http://127.0.0.1:8080", staticTokenSource{token: testToken(1)}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Push(context.Background(), team.PushBatch{BatchID: "BATCH-1"})
			var redirect *RedirectError
			if !errors.As(err, &redirect) {
				t.Fatalf("Push() error = %v, want RedirectError", err)
			}
			if transport.calls != 1 {
				t.Fatalf("redirect request calls = %d, want exactly 1", transport.calls)
			}
			if redirect.StatusCode != status {
				t.Fatalf("redirect status = %d, want %d", redirect.StatusCode, status)
			}
		})
	}
}

func TestClientMapsAPIErrorBeforePushResultDecoding(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, code: "unauthorized"},
		{name: "forbidden", status: http.StatusForbidden, code: "forbidden"},
		{name: "invalid candidate", status: http.StatusUnprocessableEntity, code: "invalid_candidate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &countingTransport{status: test.status, body: `{"code":"` + test.code + `","message":"request denied","trace_id":"TRACE-1"}`}
			client, err := NewClient("http://127.0.0.1:8080", staticTokenSource{token: testToken(1)}, &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Push(context.Background(), team.PushBatch{BatchID: "BATCH-1"})
			var api *APIError
			if !errors.As(err, &api) {
				t.Fatalf("Push() error = %v, want APIError", err)
			}
			if api.StatusCode != test.status || api.Code != test.code || api.Message != "request denied" {
				t.Fatalf("APIError = %#v, want status=%d code=%q message=%q", api, test.status, test.code, "request denied")
			}
		})
	}
}

func TestClientMapsStructuredConflictError(t *testing.T) {
	transport := &countingTransport{status: http.StatusConflict, body: `{"status":"Conflict","conflict_id":"CONFLICT-1","code":"stale_baseline","message":"stale"}`}
	client, err := NewClient("http://localhost:8080", staticTokenSource{token: testToken(1)}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Push(context.Background(), team.PushBatch{BatchID: "BATCH-1"})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Push() error = %v, want ConflictError", err)
	}
	if conflict.Result.Code != team.CodeStaleBaseline {
		t.Fatalf("code = %q", conflict.Result.Code)
	}
}

func TestClientDecodesStatusAndConflicts(t *testing.T) {
	transport := &scriptedTransport{responses: []scriptedResponse{
		{body: `{"project_id":"PRJ-TEST","team_seq":2}`},
		{body: `[{"id":"CONFLICT-1","status":"open"}]`},
	}}
	client, err := NewClient("http://127.0.0.1:8080", staticTokenSource{token: testToken(1)}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ProjectID != "PRJ-TEST" || status.TeamSeq != 2 {
		t.Fatalf("Status() = %#v, want decoded response", status)
	}
	conflicts, err := client.Conflicts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].ID != "CONFLICT-1" {
		t.Fatalf("Conflicts() = %#v, want decoded response", conflicts)
	}
}

type staticTokenSource struct{ token string }

func (s staticTokenSource) Token(context.Context) (string, error) { return s.token, nil }

type countingTransport struct {
	calls  int
	status int
	body   string
}

type scriptedResponse struct{ body string }

type scriptedTransport struct {
	responses []scriptedResponse
	calls     int
}

func (t *scriptedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	response := t.responses[t.calls]
	t.calls++
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: t.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(t.body))}, nil
}

type redirectTransport struct {
	calls  int
	status int
}

func (t *redirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{
		StatusCode: t.status,
		Header:     http.Header{"Location": []string{"/redirected"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

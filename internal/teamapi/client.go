package teamapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
)

type TokenSource interface {
	Token(context.Context) (string, error)
}

type Client struct {
	endpoint   *url.URL
	tokens     TokenSource
	httpClient *http.Client
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (errorValue *APIError) Error() string {
	return fmt.Sprintf("team API %d %s: %s", errorValue.StatusCode, errorValue.Code, errorValue.Message)
}

type ConflictError struct{ Result team.PushResult }

func (errorValue *ConflictError) Error() string {
	return fmt.Sprintf("team conflict %s: %s", errorValue.Result.Code, errorValue.Result.Message)
}

// RedirectError means a write received an HTTP redirect before its result was
// known. Callers must decide whether a new write is safe; Client never replays
// it automatically.
type RedirectError struct {
	StatusCode int
	Location   string
}

func (errorValue *RedirectError) Error() string {
	return fmt.Sprintf("team write redirect %d to %q", errorValue.StatusCode, errorValue.Location)
}

var errWriteRedirect = errors.New("team write redirect")

func NewClient(endpoint string, tokens TokenSource, httpClient *http.Client) (*Client, error) {
	if tokens == nil {
		return nil, errors.New("team token source is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("team endpoint must be an absolute URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, errors.New("team endpoint scheme must be http or https")
	}
	if parsed.Scheme == "http" && !loopbackHost(parsed.Hostname()) {
		return nil, errors.New("plain HTTP Team Core endpoint must be loopback")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{endpoint: parsed, tokens: tokens, httpClient: httpClient}, nil
}

func (client *Client) Status(ctx context.Context) (team.Status, error) {
	var status team.Status
	return status, client.get(ctx, "status", nil, &status)
}
func (client *Client) Pull(ctx context.Context, after uint64) ([]model.Event, error) {
	var response eventsResponse
	query := url.Values{}
	query.Set("after", fmt.Sprint(after))
	if err := client.get(ctx, "events", query, &response); err != nil {
		return nil, err
	}
	return response.Events, nil
}
func (client *Client) Push(ctx context.Context, batch team.PushBatch) (team.PushResult, error) {
	return client.write(ctx, "batches", batch)
}
func (client *Client) IssueLease(ctx context.Context, lease model.Lease) (team.PushResult, error) {
	return client.write(ctx, "leases", lease)
}
func (client *Client) RenewLease(ctx context.Context, id string, expiresAt time.Time) (team.PushResult, error) {
	return client.write(ctx, "leases/"+url.PathEscape(id)+"/renew", renewLeaseRequest{ExpiresAt: expiresAt})
}
func (client *Client) ReleaseLease(ctx context.Context, id string) (team.PushResult, error) {
	return client.write(ctx, "leases/"+url.PathEscape(id)+"/release", nil)
}
func (client *Client) RevokeLease(ctx context.Context, id string) (team.PushResult, error) {
	return client.write(ctx, "leases/"+url.PathEscape(id)+"/revoke", nil)
}
func (client *Client) ProposeGoalChange(ctx context.Context, change model.GoalChange) (team.PushResult, error) {
	return client.write(ctx, "goal-changes", change)
}
func (client *Client) ApproveGoalChange(ctx context.Context, id string) (team.PushResult, error) {
	return client.write(ctx, "goal-changes/"+url.PathEscape(id)+"/approve", nil)
}
func (client *Client) RejectGoalChange(ctx context.Context, id, reason string) (team.PushResult, error) {
	return client.write(ctx, "goal-changes/"+url.PathEscape(id)+"/reject", rejectGoalChangeRequest{Reason: reason})
}
func (client *Client) Conflicts(ctx context.Context) ([]model.Conflict, error) {
	var conflicts []model.Conflict
	return conflicts, client.get(ctx, "conflicts", nil, &conflicts)
}
func (client *Client) ResolveConflict(ctx context.Context, id, action string) (team.PushResult, error) {
	return client.ResolveConflictRequest(ctx, id, team.ConflictResolutionRequest{Action: action})
}
func (client *Client) ResolveConflictRequest(ctx context.Context, id string, request team.ConflictResolutionRequest) (team.PushResult, error) {
	return client.write(ctx, "conflicts/"+url.PathEscape(id)+"/resolve", resolveConflictRequest{Action: request.Action, Replacement: request.Replacement, Confirmed: request.Confirmed})
}

func (client *Client) get(ctx context.Context, path string, query url.Values, output any) error {
	response, err := client.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return responseError(response)
	}
	return decodeStrictJSON(response.Body, output)
}

func (client *Client) write(ctx context.Context, path string, input any) (team.PushResult, error) {
	response, err := client.do(ctx, http.MethodPost, path, nil, input)
	if err != nil {
		return team.PushResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		var result team.PushResult
		if err := decodeStrictJSON(response.Body, &result); err != nil {
			return team.PushResult{}, err
		}
		return result, &ConflictError{Result: result}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return team.PushResult{}, responseError(response)
	}
	var result team.PushResult
	if err := decodeStrictJSON(response.Body, &result); err != nil {
		return team.PushResult{}, err
	}
	return result, nil
}

func (client *Client) do(ctx context.Context, method, path string, query url.Values, input any) (*http.Response, error) {
	if client == nil || client.endpoint == nil || client.tokens == nil || client.httpClient == nil {
		return nil, errors.New("team client is not configured")
	}
	token, err := client.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	urlCopy := *client.endpoint
	urlCopy.Path = strings.TrimRight(urlCopy.Path, "/") + teamAPIPath + path
	urlCopy.RawQuery = query.Encode()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, urlCopy.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := client.httpClient
	if method != http.MethodGet && method != http.MethodHead {
		copyClient := *client.httpClient
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return errWriteRedirect
		}
		httpClient = &copyClient
	}
	response, err := httpClient.Do(request)
	if errors.Is(err, errWriteRedirect) {
		redirect := &RedirectError{}
		if response != nil {
			redirect.StatusCode = response.StatusCode
			redirect.Location = response.Header.Get("Location")
			_ = response.Body.Close()
		}
		return nil, redirect
	}
	return response, err
}

func responseError(response *http.Response) error {
	var api apiError
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&api); err != nil {
		return err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple values")
		}
		return err
	}
	return &APIError{StatusCode: response.StatusCode, Code: api.Code, Message: api.Message}
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

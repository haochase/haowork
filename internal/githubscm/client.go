package githubscm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIVersion = "2026-03-10"
	githubMediaType  = "application/vnd.github+json"
	maxResponseBytes = 8 << 20
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	tokens     TokenSource
	userAgent  string
	now        func() time.Time
}

type APIError struct {
	Code       string
	StatusCode int
	RetryAt    time.Time
}

func (err *APIError) Error() string {
	if err == nil {
		return ""
	}
	if err.RetryAt.IsZero() {
		return err.Code
	}
	return fmt.Sprintf("%s; retry at %s", err.Code, err.RetryAt.UTC().Format(time.RFC3339))
}

func NewClient(tokens TokenSource, httpClient *http.Client) *Client {
	baseURL, _ := url.Parse("https://api.github.com")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = sameOriginRedirect
	return &Client{
		baseURL: baseURL, httpClient: &clientCopy, tokens: tokens,
		userAgent: "haowork-github-observer/1", now: time.Now,
	}
}

func (client *Client) Repository(ctx context.Context, owner, repository, etag string) (RepositoryResult, error) {
	var result RepositoryResult
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository), nil, etag, &result.Repository)
	result.Meta = meta
	if err != nil {
		return RepositoryResult{}, err
	}
	if !meta.NotModified && (result.Repository.ID <= 0 || strings.TrimSpace(result.Repository.FullName) == "" || strings.TrimSpace(result.Repository.DefaultBranch) == "") {
		return RepositoryResult{}, errors.New("GitHub repository response is incomplete")
	}
	return result, nil
}

func (client *Client) Reference(ctx context.Context, owner, repository, ref, etag string) (ReferenceResult, error) {
	var result ReferenceResult
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "git", "ref", ref), nil, etag, &result.Reference)
	result.Meta = meta
	if err != nil {
		return ReferenceResult{}, err
	}
	if !meta.NotModified && (result.Reference.Ref == "" || result.Reference.Object.Type != "commit" || result.Reference.Object.SHA == "") {
		return ReferenceResult{}, errors.New("GitHub reference response is incomplete")
	}
	return result, nil
}

func (client *Client) PullRequests(ctx context.Context, owner, repository string, query PullQuery, etag string) (PullPageResult, error) {
	if err := validatePagination(query); err != nil {
		return PullPageResult{}, err
	}
	values := pullQueryValues(query)
	var pulls []PullRequest
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "pulls"), values, etag, &pulls)
	if err != nil {
		return PullPageResult{}, err
	}
	for _, pull := range pulls {
		if pull.Number <= 0 || pull.State == "" || pull.Base.SHA == "" || pull.Head.SHA == "" || pull.UpdatedAt.IsZero() {
			return PullPageResult{}, errors.New("GitHub pull request response is incomplete")
		}
	}
	return PullPageResult{Pulls: pulls, Meta: meta}, nil
}

func (client *Client) PullRequest(ctx context.Context, owner, repository string, number int, etag string) (PullResult, error) {
	var pull PullRequest
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "pulls", strconv.Itoa(number)), nil, etag, &pull)
	if err != nil {
		return PullResult{}, err
	}
	if !meta.NotModified && (pull.Number != number || pull.Base.SHA == "" || pull.Head.SHA == "" || pull.UpdatedAt.IsZero()) {
		return PullResult{}, errors.New("GitHub pull request response is incomplete")
	}
	return PullResult{Pull: pull, Meta: meta}, nil
}

func (client *Client) PullCommits(ctx context.Context, owner, repository string, number int, query PullQuery, etag string) (CommitPageResult, error) {
	if err := validatePagination(query); err != nil {
		return CommitPageResult{}, err
	}
	var commits []PullCommit
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "pulls", strconv.Itoa(number), "commits"), pageValues(query), etag, &commits)
	if err != nil {
		return CommitPageResult{}, err
	}
	for _, commit := range commits {
		if commit.SHA == "" {
			return CommitPageResult{}, errors.New("GitHub pull commit response is incomplete")
		}
	}
	return CommitPageResult{Commits: commits, Meta: meta}, nil
}

func (client *Client) PullReviews(ctx context.Context, owner, repository string, number int, query PullQuery, etag string) (ReviewPageResult, error) {
	if err := validatePagination(query); err != nil {
		return ReviewPageResult{}, err
	}
	var reviews []PullReview
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "pulls", strconv.Itoa(number), "reviews"), pageValues(query), etag, &reviews)
	if err != nil {
		return ReviewPageResult{}, err
	}
	for _, review := range reviews {
		if review.ID <= 0 || review.State == "" || review.CommitID == "" || review.SubmittedAt.IsZero() {
			return ReviewPageResult{}, errors.New("GitHub pull review response is incomplete")
		}
	}
	return ReviewPageResult{Reviews: reviews, Meta: meta}, nil
}

func (client *Client) CheckRuns(ctx context.Context, owner, repository, ref string, query PullQuery, etag string) (CheckPageResult, error) {
	if err := validatePagination(query); err != nil {
		return CheckPageResult{}, err
	}
	var envelope CheckRunsEnvelope
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "commits", ref, "check-runs"), pageValues(query), etag, &envelope)
	if err != nil {
		return CheckPageResult{}, err
	}
	for _, check := range envelope.CheckRuns {
		if check.ID <= 0 || check.Name == "" || check.HeadSHA == "" || check.Status == "" {
			return CheckPageResult{}, errors.New("GitHub check run response is incomplete")
		}
	}
	return CheckPageResult{CheckRuns: envelope.CheckRuns, Meta: meta}, nil
}

func (client *Client) CombinedStatus(ctx context.Context, owner, repository, ref, etag string) (StatusResult, error) {
	var status CombinedStatus
	meta, err := client.get(ctx, client.endpoint("repos", owner, repository, "commits", ref, "status"), nil, etag, &status)
	if err != nil {
		return StatusResult{}, err
	}
	if !meta.NotModified && (status.State == "" || status.SHA == "") {
		return StatusResult{}, errors.New("GitHub combined status response is incomplete")
	}
	return StatusResult{Status: status, Meta: meta}, nil
}

func (client *Client) get(ctx context.Context, endpoint *url.URL, query url.Values, etag string, destination any) (ResponseMeta, error) {
	if client == nil || client.baseURL == nil || client.httpClient == nil || client.now == nil {
		return ResponseMeta{}, errors.New("GitHub REST client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return ResponseMeta{}, err
	}
	requestURL := *endpoint
	if query != nil {
		requestURL.RawQuery = query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return ResponseMeta{}, err
	}
	request.Header.Set("Accept", githubMediaType)
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", client.userAgent)
	if strings.TrimSpace(etag) != "" {
		request.Header.Set("If-None-Match", strings.TrimSpace(etag))
	}
	if client.tokens != nil {
		token, tokenErr := client.tokens.Token(ctx)
		if tokenErr != nil {
			return ResponseMeta{}, tokenErr
		}
		if token = strings.TrimSpace(token); token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return ResponseMeta{}, &APIError{Code: "github_unavailable"}
	}
	defer response.Body.Close()
	meta, err := responseMeta(response, request.URL)
	if err != nil {
		return ResponseMeta{}, err
	}
	if response.StatusCode == http.StatusNotModified {
		meta.NotModified = true
		return meta, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ResponseMeta{}, client.apiError(response)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return ResponseMeta{}, &APIError{Code: "github_response_invalid", StatusCode: response.StatusCode}
	}
	if len(encoded) > maxResponseBytes {
		return ResponseMeta{}, &APIError{Code: "github_response_too_large", StatusCode: response.StatusCode}
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		return ResponseMeta{}, &APIError{Code: "github_response_invalid", StatusCode: response.StatusCode}
	}
	return meta, nil
}

func (client *Client) apiError(response *http.Response) error {
	code := "github_api_error"
	retryAt := retryTime(response.Header, client.now())
	switch response.StatusCode {
	case http.StatusUnauthorized:
		code = "github_auth_invalid"
	case http.StatusForbidden:
		if !retryAt.IsZero() || response.Header.Get("X-RateLimit-Remaining") == "0" {
			code = "github_rate_limited"
		} else {
			code = "github_forbidden"
		}
	case http.StatusNotFound:
		code = "remote_not_found_or_forbidden"
	case http.StatusGone:
		code = "github_api_version_retired"
	case http.StatusUnprocessableEntity:
		code = "github_validation_failed"
	case http.StatusTooManyRequests:
		code = "github_rate_limited"
	default:
		if response.StatusCode >= 500 {
			code = "github_unavailable"
		}
	}
	return &APIError{Code: code, StatusCode: response.StatusCode, RetryAt: retryAt}
}

func (client *Client) endpoint(segments ...string) *url.URL {
	result := *client.baseURL
	path := strings.TrimRight(result.Path, "/")
	for _, segment := range segments {
		path += "/" + segment
	}
	result.Path = path
	return &result
}

func responseMeta(response *http.Response, requestURL *url.URL) (ResponseMeta, error) {
	meta := ResponseMeta{ETag: response.Header.Get("ETag"), RateLimitRemaining: -1}
	if value := response.Header.Get("X-RateLimit-Remaining"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			meta.RateLimitRemaining = parsed
		}
	}
	if value := response.Header.Get("X-RateLimit-Reset"); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			meta.RateLimitReset = time.Unix(parsed, 0).UTC()
		}
	}
	next, err := nextLink(response.Header.Get("Link"), requestURL)
	if err != nil {
		return ResponseMeta{}, err
	}
	meta.NextURL = next
	return meta, nil
}

func nextLink(header string, requestURL *url.URL) (string, error) {
	if strings.TrimSpace(header) == "" {
		return "", nil
	}
	for _, entry := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(entry), ";")
		if len(parts) < 2 || !strings.Contains(strings.Join(parts[1:], ";"), `rel="next"`) {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(parts[0]), "<>")
		parsed, err := url.Parse(raw)
		if err != nil || !sameOrigin(parsed, requestURL) {
			return "", &APIError{Code: "github_pagination_invalid"}
		}
		return parsed.String(), nil
	}
	return "", nil
}

func sameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func sameOriginRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= 10 || !sameOrigin(request.URL, via[0].URL) {
		return errors.New("GitHub redirect is outside the configured origin")
	}
	return nil
}

func retryTime(header http.Header, now time.Time) time.Time {
	if value := strings.TrimSpace(header.Get("Retry-After")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return now.Add(time.Duration(seconds) * time.Second).UTC()
		}
		if parsed, err := http.ParseTime(value); err == nil {
			return parsed.UTC()
		}
	}
	if value := strings.TrimSpace(header.Get("X-RateLimit-Reset")); value != "" {
		if epoch, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.Unix(epoch, 0).UTC()
		}
	}
	return time.Time{}
}

func pullQueryValues(query PullQuery) url.Values {
	values := pageValues(query)
	for key, value := range map[string]string{"state": query.State, "head": query.Head, "base": query.Base, "sort": query.Sort, "direction": query.Direction} {
		if strings.TrimSpace(value) != "" {
			values.Set(key, value)
		}
	}
	return values
}

func pageValues(query PullQuery) url.Values {
	values := make(url.Values)
	if query.PerPage > 0 {
		values.Set("per_page", strconv.Itoa(query.PerPage))
	}
	if query.Page > 0 {
		values.Set("page", strconv.Itoa(query.Page))
	}
	return values
}

func validatePagination(query PullQuery) error {
	if query.PerPage < 0 || query.PerPage > 100 || query.Page < 0 {
		return errors.New("GitHub pagination parameters are invalid")
	}
	return nil
}

package corebridge

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

	"github.com/haochase/haowork/internal/executionreceipt"
)

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

var ErrEvidenceNotFound = errors.New("Core Bridge run evidence is not found")

const clientMaxBodySize int64 = 1 << 20
const defaultClientTimeout = 4 * time.Minute

func NewClient(baseURL, token string, client *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || strings.TrimSpace(token) == "" {
		return nil, errors.New("Core Bridge URL and token are required")
	}
	if base.Scheme == "http" && !isLoopbackCoreBridgeHost(base.Hostname()) {
		return nil, errors.New("Core Bridge HTTP is allowed only through a loopback port-forward")
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 {
		copy.Timeout = defaultClientTimeout
	}
	return &Client{base: base, token: strings.TrimSpace(token), http: &copy}, nil
}

func isLoopbackCoreBridgeHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (client *Client) Start(ctx context.Context, input StartRequest) (RunEvidence, error) {
	return client.start(ctx, input, false)
}

// StartNew atomically requires that neither durable evidence nor an active
// reservation exists for the supplied RunID.
func (client *Client) StartNew(ctx context.Context, input StartRequest) (RunEvidence, error) {
	return client.start(ctx, input, true)
}

func (client *Client) start(ctx context.Context, input StartRequest, requireNew bool) (RunEvidence, error) {
	if err := ValidateRunID(input.Request.RunID); err != nil {
		return RunEvidence{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return RunEvidence{}, err
	}
	return client.do(ctx, http.MethodPost, "/v1/runs/start", encoded, input.Request.RunID, requireNew)
}

func (client *Client) Evidence(ctx context.Context, runID string) (RunEvidence, error) {
	if err := ValidateRunID(runID); err != nil {
		return RunEvidence{}, err
	}
	return client.do(ctx, http.MethodGet, "/v1/runs/"+url.PathEscape(runID)+"/evidence", nil, runID, false)
}

func (client *Client) do(ctx context.Context, method, path string, body []byte, runID string, requireNew bool) (RunEvidence, error) {
	if client == nil || client.base == nil || client.http == nil {
		return RunEvidence{}, errors.New("Core Bridge client is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.base.String(), "/")+path, bytes.NewReader(body))
	if err != nil {
		return RunEvidence{}, err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if requireNew {
		request.Header.Set("X-Haowork-Require-New", "true")
	}
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return RunEvidence{}, err
	}
	defer response.Body.Close()
	if method == http.MethodGet && response.StatusCode == http.StatusNotFound {
		return RunEvidence{}, ErrEvidenceNotFound
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return RunEvidence{}, fmt.Errorf("Core Bridge %s returned %s", path, response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, clientMaxBodySize+1))
	if err != nil {
		return RunEvidence{}, err
	}
	if int64(len(content)) > clientMaxBodySize {
		return RunEvidence{}, errors.New("Core Bridge evidence exceeds maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var evidence RunEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return RunEvidence{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RunEvidence{}, errors.New("Core Bridge evidence has trailing data")
	}
	if evidence.RunID != runID || len(evidence.ExecutionReceiptIDs) != 1 || !executionreceipt.ValidReceiptID(evidence.ExecutionReceiptIDs[0]) || len(evidence.SourceEventIDs) != 1 || evidence.SourceEventIDs[0] != evidence.ExecutionReceiptIDs[0] {
		return RunEvidence{}, errors.New("Core Bridge returned invalid execution receipt evidence")
	}
	return evidence, nil
}

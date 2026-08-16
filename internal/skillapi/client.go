package skillapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
)

// RequestSigner signs a bounded cross-zone payload. It has no file-system capability.
type RequestSigner interface {
	Sign(context.Context, []byte) (string, error)
}

// NewSignedRequest prepares a transfer request for a caller-owned transport. It never follows redirects or sends bytes.
func NewSignedRequest(ctx context.Context, method, endpoint string, payload []byte, signer RequestSigner) (*http.Request, error) {
	if signer == nil || strings.TrimSpace(endpoint) == "" || strings.TrimSpace(method) == "" {
		return nil, errors.New("method, endpoint, and signer are required")
	}
	signature, err := signer.Sign(ctx, payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Haowork-Signature", signature)
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

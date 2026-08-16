package agentteamsbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

var ErrArtifactDigest = errors.New("artifact SHA-256 mismatch")

type ArtifactStore interface {
	Upload(context.Context, string, []byte, string) (string, error)
	Download(context.Context, string) ([]byte, error)
}

type HTTPArtifactStore struct {
	base     *url.URL
	client   *http.Client
	identity string
	maxBytes int64
}

func NewHTTPArtifactStore(baseURL string, client *http.Client, identity string) *HTTPArtifactStore {
	base, _ := url.Parse(strings.TrimRight(baseURL, "/"))
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPArtifactStore{base: base, client: &copy, identity: strings.TrimSpace(identity), maxBytes: 16 << 20}
}

func (store *HTTPArtifactStore) Upload(ctx context.Context, ref string, data []byte, digest string) (string, error) {
	if store == nil {
		return "", ErrInsecureControlPlane
	}
	if err := validateArtifactPath(ref); err != nil {
		return "", err
	}
	if int64(len(data)) > store.maxBytes {
		return "", errors.New("artifact exceeds maximum size")
	}
	computed := sha256.Sum256(data)
	computedDigest := hex.EncodeToString(computed[:])
	if len(digest) != 64 {
		return "", errors.New("artifact digest must be a SHA-256 hex string")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || !strings.EqualFold(digest, computedDigest) {
		return "", ErrArtifactDigest
	}
	if err := store.do(ctx, http.MethodPut, ref, data, computedDigest, nil); err != nil {
		return "", err
	}
	return ref, nil
}

func (store *HTTPArtifactStore) Download(ctx context.Context, ref string) ([]byte, error) {
	if err := validateArtifactPath(ref); err != nil {
		return nil, err
	}
	var data []byte
	if err := store.do(ctx, http.MethodGet, ref, nil, "", &data); err != nil {
		return nil, err
	}
	return data, nil
}

func (store *HTTPArtifactStore) do(ctx context.Context, method, ref string, body []byte, digest string, output *[]byte) error {
	if store == nil || store.base == nil || (store.base.Scheme != "https" && !isLoopbackHost(store.base.Host)) {
		return ErrInsecureControlPlane
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(store.base.String(), "/")+"/"+ref, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if digest != "" {
		req.Header.Set("X-Artifact-SHA256", digest)
	}
	response, err := store.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if store.identity != "" && response.Header.Get("X-AgentTeams-Identity") != store.identity {
		return ErrIdentityMismatch
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("artifact %s %s returned %s", method, ref, response.Status)
	}
	if output != nil {
		*output, err = io.ReadAll(io.LimitReader(response.Body, store.maxBytes+1))
		if err != nil {
			return err
		}
		if int64(len(*output)) > store.maxBytes {
			return errors.New("artifact exceeds maximum size")
		}
	}
	return nil
}

func validateArtifactPath(ref string) error {
	parsed, err := url.Parse(ref)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(ref, "/") || strings.Contains(path.Clean(ref), "..") || strings.TrimSpace(ref) == "" {
		return errors.New("artifact reference must be a relative path")
	}
	return nil
}

func DownloadMatrixArtifacts(ctx context.Context, store ArtifactStore, artifacts []MatrixArtifact, environment string, maxBytes int64) ([]model.ArtifactRef, error) {
	if maxBytes <= 0 {
		maxBytes = 16 << 20
	}
	refs := make([]model.ArtifactRef, 0, len(artifacts))
	for _, artifact := range artifacts {
		uri, err := url.Parse(artifact.URI)
		if err != nil || uri.IsAbs() || uri.Host != "" || strings.HasPrefix(artifact.URI, "/") || strings.Contains(path.Clean(artifact.URI), "..") || artifact.Size < 0 || artifact.Size > maxBytes || artifact.EnvironmentID != environment || len(artifact.SHA256) != 64 {
			return nil, fmt.Errorf("invalid Matrix artifact reference")
		}
		data, err := VerifyArtifactDownload(ctx, store, artifact.URI, artifact.SHA256)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != artifact.Size {
			return nil, fmt.Errorf("Matrix artifact size mismatch")
		}
		refs = append(refs, model.ArtifactRef{Kind: artifact.Kind, URI: artifact.URI, SHA256: artifact.SHA256})
	}
	return refs, nil
}

func VerifyArtifactDownload(ctx context.Context, store ArtifactStore, ref, wantSHA256 string) ([]byte, error) {
	data, err := store.Download(ctx, ref)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != wantSHA256 {
		return nil, ErrArtifactDigest
	}
	return data, nil
}

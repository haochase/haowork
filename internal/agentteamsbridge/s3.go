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
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3ObjectInfo contains only verifiable object metadata returned by a storage
// adapter. It is deliberately small so tests do not need an S3 service.
type S3ObjectInfo struct {
	Key       string
	Size      int64
	VersionID string
}

// S3ObjectClient is implemented by the official MinIO SDK adapter below and
// is injectable only for deterministic tests.
type S3ObjectClient interface {
	PutObject(context.Context, string, string, io.Reader, int64) (S3ObjectInfo, error)
	StatObject(context.Context, string, string) (S3ObjectInfo, error)
	GetObject(context.Context, string, string) (io.ReadCloser, error)
}

type S3ArtifactStoreConfig struct {
	Endpoint      string
	AccessKey     string
	SecretKey     string
	SessionToken  string
	Secure        bool
	HTTPTransport http.RoundTripper
	Bucket        string
	EnvironmentID string
	MissionID     string
	MaxBytes      int64
}

// S3ArtifactStore is the official S3-compatible ArtifactStore. A store is
// permanently scoped to a single environment and mission so a cross-zone
// object reference cannot be followed accidentally.
type S3ArtifactStore struct {
	client        S3ObjectClient
	bucket        string
	environmentID string
	missionID     string
	maxBytes      int64
}

const defaultS3ArtifactMaxBytes int64 = 16 << 20

// NewS3ArtifactStore creates an official MinIO Go SDK client. Credentials are
// supplied by the caller from a mounted Secret or controlled environment; they
// are not saved in the ArtifactStore or returned to the caller.
func NewS3ArtifactStore(config S3ArtifactStoreConfig) (*S3ArtifactStore, error) {
	if strings.TrimSpace(config.Endpoint) == "" || strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return nil, errors.New("S3 endpoint and credentials are required")
	}
	client, err := minio.New(strings.TrimSpace(config.Endpoint), &minio.Options{
		Creds:     credentials.NewStaticV4(config.AccessKey, config.SecretKey, config.SessionToken),
		Secure:    config.Secure,
		Transport: config.HTTPTransport,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return NewS3ArtifactStoreWithClient(config, minioS3Client{client: client})
}

func NewS3ArtifactStoreWithClient(config S3ArtifactStoreConfig, client S3ObjectClient) (*S3ArtifactStore, error) {
	if client == nil || strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.EnvironmentID) == "" || strings.TrimSpace(config.MissionID) == "" {
		return nil, errors.New("S3 client, bucket, environment, and mission are required")
	}
	maxBytes := config.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultS3ArtifactMaxBytes
	}
	return &S3ArtifactStore{client: client, bucket: strings.TrimSpace(config.Bucket), environmentID: strings.TrimSpace(config.EnvironmentID), missionID: strings.TrimSpace(config.MissionID), maxBytes: maxBytes}, nil
}

func (store *S3ArtifactStore) Upload(ctx context.Context, ref string, data []byte, digest string) (string, error) {
	if store == nil || store.client == nil {
		return "", errors.New("S3 artifact store is required")
	}
	if err := validateArtifactPath(ref); err != nil {
		return "", err
	}
	if int64(len(data)) > store.maxBytes {
		return "", errors.New("artifact exceeds maximum size")
	}
	digest, err := normalizedSHA256(digest)
	if err != nil {
		return "", err
	}
	computed := sha256.Sum256(data)
	if digest != hex.EncodeToString(computed[:]) {
		return "", ErrArtifactDigest
	}
	key := store.artifactKey(digest)
	info, err := store.client.PutObject(ctx, store.bucket, key, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if info.Key != key || info.Size != int64(len(data)) {
		return "", errors.New("S3 upload response diverges from requested object")
	}
	return key, nil
}

func (store *S3ArtifactStore) Download(ctx context.Context, key string) ([]byte, error) {
	if store == nil || store.client == nil {
		return nil, errors.New("S3 artifact store is required")
	}
	digest, err := store.validateKey(key)
	if err != nil {
		return nil, err
	}
	info, err := store.client.StatObject(ctx, store.bucket, key)
	if err != nil {
		return nil, err
	}
	if info.Key != "" && info.Key != key || info.Size < 0 || info.Size > store.maxBytes {
		return nil, errors.New("S3 object metadata is invalid")
	}
	object, err := store.client.GetObject(ctx, store.bucket, key)
	if err != nil {
		return nil, err
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, store.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size {
		return nil, errors.New("S3 object size diverges from metadata")
	}
	computed := sha256.Sum256(data)
	if hex.EncodeToString(computed[:]) != digest {
		return nil, ErrArtifactDigest
	}
	return data, nil
}

func (store *S3ArtifactStore) artifactKey(digest string) string {
	return "environments/" + store.environmentID + "/missions/" + store.missionID + "/artifacts/" + digest
}

func (store *S3ArtifactStore) validateKey(key string) (string, error) {
	if err := validateArtifactPath(key); err != nil {
		return "", err
	}
	prefix := "environments/" + store.environmentID + "/missions/" + store.missionID + "/artifacts/"
	if !strings.HasPrefix(key, prefix) {
		return "", errors.New("S3 artifact key is outside the current environment or mission")
	}
	digest := strings.TrimPrefix(key, prefix)
	if strings.Contains(digest, "/") {
		return "", errors.New("S3 artifact key has an invalid digest suffix")
	}
	return normalizedSHA256(digest)
}

func normalizedSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || len(value) != sha256.Size*2 {
		return "", errors.New("artifact digest must be a SHA-256 hex string")
	}
	return value, nil
}

type minioS3Client struct{ client *minio.Client }

func (client minioS3Client) PutObject(ctx context.Context, bucket, key string, reader io.Reader, size int64) (S3ObjectInfo, error) {
	info, err := client.client.PutObject(ctx, bucket, key, reader, size, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return S3ObjectInfo{}, err
	}
	return S3ObjectInfo{Key: info.Key, Size: info.Size, VersionID: info.VersionID}, nil
}

func (client minioS3Client) StatObject(ctx context.Context, bucket, key string) (S3ObjectInfo, error) {
	info, err := client.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return S3ObjectInfo{}, err
	}
	return S3ObjectInfo{Key: info.Key, Size: info.Size, VersionID: info.VersionID}, nil
}

func (client minioS3Client) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	object, err := client.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return object, nil
}

var _ ArtifactStore = (*S3ArtifactStore)(nil)

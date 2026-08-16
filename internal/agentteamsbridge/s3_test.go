package agentteamsbridge_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/agentteamsbridge"
)

func TestS3ArtifactRoundTripVerifiesBucketKeySizeAndSHA256(t *testing.T) {
	client := &memoryS3Client{objects: map[string][]byte{}}
	store, err := agentteamsbridge.NewS3ArtifactStoreWithClient(agentteamsbridge.S3ArtifactStoreConfig{
		Bucket: "haowork-public-artifacts", EnvironmentID: "public", MissionID: "MSN-001", MaxBytes: 1024,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("patch artifact")
	digest := sha256.Sum256(data)
	key, err := store.Upload(context.Background(), "reports/patch.json", data, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	wantKey := "environments/public/missions/MSN-001/artifacts/" + hex.EncodeToString(digest[:])
	if key != wantKey || client.lastBucket != "haowork-public-artifacts" || client.lastKey != wantKey || client.putCalls != 1 {
		t.Fatalf("S3 upload bucket/key = %q/%q, returned %q", client.lastBucket, client.lastKey, key)
	}
	got, err := store.Download(context.Background(), key)
	if err != nil || !bytes.Equal(got, data) || client.statCalls != 1 || client.getCalls != 1 {
		t.Fatalf("S3 download = %q, err=%v, stats=%d gets=%d", got, err, client.statCalls, client.getCalls)
	}
}

func TestS3ArtifactRejectsCrossEnvironmentKeyAndResponseLossDivergence(t *testing.T) {
	data := []byte("patch artifact")
	digest := sha256.Sum256(data)
	client := &memoryS3Client{objects: map[string][]byte{}, putResult: agentteamsbridge.S3ObjectInfo{Key: "divergent-key", Size: int64(len(data))}}
	store, err := agentteamsbridge.NewS3ArtifactStoreWithClient(agentteamsbridge.S3ArtifactStoreConfig{
		Bucket: "haowork-public-artifacts", EnvironmentID: "public", MissionID: "MSN-001", MaxBytes: 1024,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	foreignKey := "environments/internal/missions/MSN-001/artifacts/" + hex.EncodeToString(digest[:])
	if _, err := store.Download(context.Background(), foreignKey); err == nil {
		t.Fatal("S3 Download accepted a cross-environment key")
	}
	if client.statCalls != 0 || client.getCalls != 0 {
		t.Fatalf("cross-environment download touched S3: stat=%d get=%d", client.statCalls, client.getCalls)
	}
	if _, err := store.Upload(context.Background(), "reports/patch.json", data, hex.EncodeToString(digest[:])); err == nil {
		t.Fatal("S3 Upload accepted a divergent put response")
	}
	if client.putCalls != 1 {
		t.Fatalf("S3 Upload retried after divergent response: calls=%d", client.putCalls)
	}
}

type memoryS3Client struct {
	objects             map[string][]byte
	putCalls, getCalls  int
	statCalls           int
	lastBucket, lastKey string
	putResult           agentteamsbridge.S3ObjectInfo
	putErr, getErr      error
	statErr             error
	statOverride        agentteamsbridge.S3ObjectInfo
}

func (client *memoryS3Client) PutObject(_ context.Context, bucket, key string, reader io.Reader, size int64) (agentteamsbridge.S3ObjectInfo, error) {
	client.putCalls++
	client.lastBucket, client.lastKey = bucket, key
	if client.putErr != nil {
		return agentteamsbridge.S3ObjectInfo{}, client.putErr
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return agentteamsbridge.S3ObjectInfo{}, err
	}
	if int64(len(data)) != size {
		return agentteamsbridge.S3ObjectInfo{}, errors.New("unexpected size")
	}
	client.objects[key] = append([]byte(nil), data...)
	if client.putResult.Key != "" {
		return client.putResult, nil
	}
	return agentteamsbridge.S3ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

func (client *memoryS3Client) StatObject(_ context.Context, bucket, key string) (agentteamsbridge.S3ObjectInfo, error) {
	client.statCalls++
	client.lastBucket, client.lastKey = bucket, key
	if client.statErr != nil {
		return agentteamsbridge.S3ObjectInfo{}, client.statErr
	}
	if client.statOverride.Key != "" {
		return client.statOverride, nil
	}
	data, exists := client.objects[key]
	if !exists {
		return agentteamsbridge.S3ObjectInfo{}, errors.New("not found")
	}
	return agentteamsbridge.S3ObjectInfo{Key: key, Size: int64(len(data))}, nil
}

func (client *memoryS3Client) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	client.getCalls++
	client.lastBucket, client.lastKey = bucket, key
	if client.getErr != nil {
		return nil, client.getErr
	}
	data, exists := client.objects[key]
	if !exists {
		return nil, errors.New("not found")
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

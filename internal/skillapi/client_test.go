package skillapi

import (
	"context"
	"io"
	"testing"
)

func TestNewSignedRequestRetainsExactlyTheSignedPayloadWithoutSending(t *testing.T) {
	signer := &recordingSigner{}
	payload := []byte(`{"capsule":"digest"}`)

	request, err := NewSignedRequest(context.Background(), "POST", "https://transfer.example/capsules", payload, signer)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(signer.payload) != string(payload) || string(body) != string(payload) || request.Header.Get("X-Haowork-Signature") != "signature" {
		t.Fatalf("signed/request payload = %q/%q; signature=%q", signer.payload, body, request.Header.Get("X-Haowork-Signature"))
	}
}

type recordingSigner struct{ payload []byte }

func (signer *recordingSigner) Sign(_ context.Context, payload []byte) (string, error) {
	signer.payload = append([]byte(nil), payload...)
	return "signature", nil
}

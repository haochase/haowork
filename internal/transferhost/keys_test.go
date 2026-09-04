package transferhost

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateKeyPairCreatesOwnerOnlyEd25519PEM(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private.pem")
	publicPath := filepath.Join(root, "public.pem")

	if err := GenerateKeyPair(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	privateKey, err := LoadPrivateKey(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := LoadPublicKey(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
		t.Fatal("generated private and public keys do not match")
	}
	if err := ValidateOwnerOnlyFile(privatePath); err != nil {
		t.Fatalf("private key security = %v", err)
	}
	if err := ValidateOwnerOnlyFile(publicPath); err != nil {
		t.Fatalf("public key security = %v", err)
	}
}

func TestGenerateKeyPairNeverOverwritesExistingOutput(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private.pem")
	publicPath := filepath.Join(root, "public.pem")
	if err := os.WriteFile(privatePath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := GenerateKeyPair(privatePath, publicPath); err == nil {
		t.Fatal("GenerateKeyPair() overwrote an existing private key")
	}
	content, err := os.ReadFile(privatePath)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing private key changed: %q, %v", content, err)
	}
	if _, err := os.Stat(publicPath); !os.IsNotExist(err) {
		t.Fatalf("public key exists after partial failure: %v", err)
	}
}

func TestLoadPrivateKeyRejectsNonEd25519PEM(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rsa.pem")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		t.Fatal(err)
	}
	secureOwnerOnlyForTest(t, path)

	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("LoadPrivateKey() accepted an RSA key")
	}
}

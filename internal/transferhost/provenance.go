package transferhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"regexp"
	"strings"

	"github.com/haochase/haowork/internal/transfer"
)

var lowercaseSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

var staticProvenanceSources = map[string]struct{}{
	"artifact-store":    {},
	"context-store":     {},
	"git":               {},
	"governance-ledger": {},
	"identity-store":    {},
	"skill-registry":    {},
}

type exactProvenanceVerifier struct {
	allowed map[string]struct{}
}

func (verifier exactProvenanceVerifier) VerifyProvenance(ctx context.Context, entry transfer.Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := sha256.Sum256(entry.Data)
	actual := hex.EncodeToString(digest[:])
	if entry.Provenance.SHA256 != actual {
		return errors.New("entry provenance digest does not match its bytes")
	}
	key := provenanceKey(entry.Provenance.Source, entry.Path, actual)
	if _, ok := verifier.allowed[key]; !ok {
		return errors.New("entry provenance is not in the trusted allowlist")
	}
	return nil
}

func validateProvenanceEntry(entry provenanceEntry) error {
	entry.Source = strings.TrimSpace(entry.Source)
	entry.Path = strings.TrimSpace(entry.Path)
	entry.SHA256 = strings.TrimSpace(entry.SHA256)
	if _, ok := staticProvenanceSources[entry.Source]; !ok {
		return errors.New("static provenance source is not supported")
	}
	if entry.Path == "" || strings.HasPrefix(entry.Path, "/") || strings.Contains(entry.Path, `\`) || path.Clean(entry.Path) != entry.Path || strings.HasPrefix(entry.Path, "../") {
		return errors.New("static provenance path is unsafe")
	}
	if !lowercaseSHA256.MatchString(entry.SHA256) {
		return errors.New("static provenance SHA-256 is invalid")
	}
	return nil
}

func provenanceKey(source, entryPath, digest string) string {
	return source + "\x00" + entryPath + "\x00" + digest
}

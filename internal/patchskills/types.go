// Package patchskills exposes cross-zone operations as signed requests only.
package patchskills

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

const SignatureAlgorithm = "Ed25519"

type Request struct {
	MissionID, EnvironmentID, SkillName, SkillVersion string
	InputSHA256                                       string
	LogicalActorID, BuildAgentID, VerifyAgentID       string
	RuntimeBindingRevision                            int
	AgentFunction                                     model.AgentFunction
	Scope                                             []string
	Audit                                             AuditFacts
	CreatedAt                                         time.Time
}
type AuditFacts struct {
	Command, WorkspaceDigest, ArtifactHash string
	ExitCode                               int
}
type Signer interface {
	KeyID() string
	Algorithm() string
	Sign([]byte) ([]byte, error)
}
type Ed25519Signer struct {
	keyID      string
	privateKey ed25519.PrivateKey
}

func NewEd25519Signer(keyID string, privateKey ed25519.PrivateKey) Ed25519Signer {
	return Ed25519Signer{keyID: keyID, privateKey: append(ed25519.PrivateKey(nil), privateKey...)}
}
func (signer Ed25519Signer) KeyID() string     { return signer.keyID }
func (signer Ed25519Signer) Algorithm() string { return SignatureAlgorithm }
func (signer Ed25519Signer) Sign(digest []byte) ([]byte, error) {
	if signer.keyID == "" || len(signer.privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("cross-zone signer is not configured")
	}
	return ed25519.Sign(signer.privateKey, digest), nil
}

type SignedRequest struct {
	Request   Request `json:"request"`
	KeyID     string  `json:"key_id"`
	Algorithm string  `json:"algorithm"`
	Digest    string  `json:"digest"`
	Signature string  `json:"signature"`
}

func (request SignedRequest) Verify(publicKey ed25519.PublicKey) error {
	if request.Algorithm != SignatureAlgorithm || request.KeyID == "" || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid signed cross-zone request metadata")
	}
	canonical, err := canonicalRequest(request.Request)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if request.Digest != hex.EncodeToString(digest[:]) {
		return errors.New("cross-zone request digest mismatch")
	}
	signature, err := base64.StdEncoding.DecodeString(request.Signature)
	if err != nil || !ed25519.Verify(publicKey, digest[:], signature) {
		return errors.New("cross-zone request signature is invalid")
	}
	return nil
}

func canonicalRequest(request Request) ([]byte, error) {
	request.MissionID = strings.TrimSpace(request.MissionID)
	request.EnvironmentID = strings.TrimSpace(request.EnvironmentID)
	request.SkillName = strings.TrimSpace(request.SkillName)
	request.SkillVersion = strings.TrimSpace(request.SkillVersion)
	request.InputSHA256 = strings.TrimSpace(request.InputSHA256)
	request.LogicalActorID = strings.TrimSpace(request.LogicalActorID)
	request.BuildAgentID = strings.TrimSpace(request.BuildAgentID)
	request.VerifyAgentID = strings.TrimSpace(request.VerifyAgentID)
	request.Scope = sortedStrings(request.Scope)
	request.CreatedAt = request.CreatedAt.UTC()
	if request.MissionID == "" || request.EnvironmentID == "" || request.SkillName == "" || request.SkillVersion == "" || request.InputSHA256 == "" || request.LogicalActorID == "" || request.BuildAgentID == "" || request.VerifyAgentID == "" || request.RuntimeBindingRevision <= 0 || request.AgentFunction == "" || len(request.Scope) == 0 || request.CreatedAt.IsZero() {
		return nil, errors.New("cross-zone request binding is required")
	}
	return json.Marshal(request)
}

func ValidatePatchScope(allowed, paths []string) error {
	if len(allowed) == 0 || len(paths) == 0 {
		return errors.New("patch scope and paths are required")
	}
	for _, path := range paths {
		path = strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
		if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, ":") || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
			return errors.New("patch path is outside workspace")
		}
		matched := false
		for _, scope := range allowed {
			scope = strings.Trim(strings.ReplaceAll(strings.TrimSpace(scope), "\\", "/"), "/")
			if strings.HasSuffix(scope, "/**") && strings.HasPrefix(path, strings.TrimSuffix(scope, "**")) || scope == path {
				matched = true
			}
		}
		if !matched {
			return errors.New("patch path is outside mission scope")
		}
	}
	return nil
}

func ValidateAudit(buildLogicalID, auditLogicalID string) error {
	if strings.TrimSpace(buildLogicalID) == "" || strings.TrimSpace(auditLogicalID) == "" || strings.TrimSpace(buildLogicalID) == strings.TrimSpace(auditLogicalID) {
		return errors.New("audit logical identity must differ from build")
	}
	return nil
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

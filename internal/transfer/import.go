package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

func (service Service) PreviewImport(ctx context.Context, archive []byte) (ImportPreview, error) {
	manifest, signature, files, err := readArchive(archive)
	if err != nil {
		return ImportPreview{}, err
	}
	if err := validateManifest(&manifest); err != nil {
		return ImportPreview{}, err
	}
	if service.TargetEnvironmentID == "" || manifest.TargetEnvironmentID != service.TargetEnvironmentID {
		return ImportPreview{}, errors.New("transfer target environment mismatch")
	}
	if service.ExpectedGoalVersion > 0 && manifest.GoalVersion != service.ExpectedGoalVersion {
		return ImportPreview{}, errors.New("transfer goal version mismatch")
	}
	if service.ExpectedGitBaseline != "" && manifest.GitBaseline != service.ExpectedGitBaseline {
		return ImportPreview{}, errors.New("transfer git baseline mismatch")
	}
	if service.ExpectedContextHash != "" && manifest.ContextHash != service.ExpectedContextHash {
		return ImportPreview{}, errors.New("transfer context hash mismatch")
	}
	if service.ExpectedLeaseID != "" && manifest.LeaseID != service.ExpectedLeaseID {
		return ImportPreview{}, errors.New("transfer lease mismatch")
	}
	if len(service.ExpectedScope) > 0 && !sameStrings(manifest.Scope, service.ExpectedScope) {
		return ImportPreview{}, errors.New("transfer scope mismatch")
	}
	if service.Now != nil && !service.Now().UTC().Before(manifest.ExpiresAt) {
		return ImportPreview{}, errors.New("transfer is expired")
	}
	if err := verifyArchive(manifest, signature, files, service.PublicKeys); err != nil {
		return ImportPreview{}, err
	}
	for name, version := range service.RequiredSkills {
		if !hasSkill(manifest.Skills, name, version) {
			return ImportPreview{}, errors.New("transfer skill version is unavailable")
		}
	}
	if service.RuntimeBindingResolver == nil || len(manifest.Agents) == 0 || manifest.LeaseID == "" || len(manifest.TraceIDs) == 0 {
		return ImportPreview{}, errors.New("transfer lease, trace, and logical identity are required")
	}
	candidates := make([]RebindCandidate, 0, len(manifest.Agents))
	for _, agent := range manifest.Agents {
		if agent.LogicalActorID == "" || len(agent.Bindings) == 0 {
			return ImportPreview{}, errors.New("transfer runtime binding history is invalid")
		}
		target, err := service.RuntimeBindingResolver.ResolveRuntimeBinding(ctx, agent.LogicalActorID, manifest.TargetEnvironmentID)
		if err != nil || target.LogicalActorID != agent.LogicalActorID || target.EnvironmentID != manifest.TargetEnvironmentID || target.RuntimePrincipalID == "" || target.AgentTeamsInstanceID == "" {
			return ImportPreview{}, errors.New("target runtime binding is unavailable")
		}
		candidates = append(candidates, RebindCandidate{LogicalActorID: agent.LogicalActorID, SourceEnvironmentID: manifest.SourceEnvironmentID, TargetEnvironmentID: manifest.TargetEnvironmentID, PreviousRevision: target.Revision, NewRevision: target.Revision + 1, RuntimePrincipalID: target.RuntimePrincipalID, AgentTeamsInstanceID: target.AgentTeamsInstanceID})
	}
	previewData, _ := json.Marshal(struct {
		Manifest   Manifest
		Candidates []RebindCandidate
	}{manifest, candidates})
	digest := sha256.Sum256(previewData)
	return ImportPreview{Manifest: manifest, RebindRequired: candidates, PreviewHash: hex.EncodeToString(digest[:]), archive: append([]byte(nil), archive...)}, nil
}

func (service Service) ApplyImport(ctx context.Context, preview ImportPreview, approval Approval) error {
	if approval.PreviewHash != preview.PreviewHash || approval.Actor.Kind != model.ActorHuman || approval.Actor.Role != model.RoleOwner {
		return errors.New("import requires owner approval bound to preview hash")
	}
	if service.Writer == nil {
		return errors.New("import writer is required")
	}
	fresh, err := service.PreviewImport(ctx, preview.archive)
	if err != nil {
		return err
	}
	if fresh.PreviewHash != preview.PreviewHash {
		return errors.New("import preview changed before approval")
	}
	for _, candidate := range fresh.RebindRequired {
		if err := service.Writer.CommitImport(ctx, candidate, fresh.PreviewHash, approval.Actor); err != nil {
			return err
		}
	}
	return nil
}

func readArchive(data []byte) (Manifest, Signature, map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Manifest{}, Signature{}, nil, err
	}
	files := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		if file.FileInfo().IsDir() || file.Name == "" || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "\\") || strings.HasPrefix(file.Name, "../") || file.Name != strings.TrimPrefix(file.Name, "./") {
			return Manifest{}, Signature{}, nil, errors.New("archive path is unsafe")
		}
		if _, exists := files[file.Name]; exists {
			return Manifest{}, Signature{}, nil, errors.New("archive contains duplicate path")
		}
		stream, err := file.Open()
		if err != nil {
			return Manifest{}, Signature{}, nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(stream, 16<<20))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil {
			return Manifest{}, Signature{}, nil, errors.New("archive entry cannot be read")
		}
		files[file.Name] = content
	}
	var manifest Manifest
	var signature Signature
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		return Manifest{}, Signature{}, nil, errors.New("archive manifest is invalid")
	}
	if err := json.Unmarshal(files["signature.json"], &signature); err != nil {
		return Manifest{}, Signature{}, nil, errors.New("archive signature is invalid")
	}
	delete(files, "manifest.json")
	delete(files, "signature.json")
	return manifest, signature, files, nil
}
func verifyArchive(manifest Manifest, signature Signature, files map[string][]byte, keys map[string]ed25519.PublicKey) error {
	if signature.Algorithm != SignatureAlgorithm || signature.KeyID == "" {
		return errors.New("archive signature metadata is invalid")
	}
	key := keys[signature.KeyID]
	if len(key) != ed25519.PublicKeySize {
		return errors.New("archive signing key is unknown")
	}
	keyDigest := sha256.Sum256(key)
	if manifest.SignatureAlgorithm != SignatureAlgorithm || manifest.PublicKeyFingerprint != hex.EncodeToString(keyDigest[:]) {
		return errors.New("archive signer fingerprint mismatch")
	}
	if len(files) != len(manifest.FileSHA256) || manifest.ContentSHA256 != contentDigest(manifest.FileSHA256) {
		return errors.New("archive content digest mismatch")
	}
	for name, expected := range manifest.FileSHA256 {
		if err := validateArchivePath(name); err != nil {
			return err
		}
		content, exists := files[name]
		if !exists {
			return errors.New("archive entry is missing")
		}
		actual := sha256.Sum256(content)
		if expected != hex.EncodeToString(actual[:]) {
			return errors.New("archive entry hash mismatch")
		}
	}
	canonical, err := canonicalManifest(manifest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	if signature.Digest != hex.EncodeToString(digest[:]) || manifest.SignatureDigest != signature.Digest {
		return errors.New("archive manifest digest mismatch")
	}
	signed, err := base64.StdEncoding.DecodeString(signature.Signature)
	if err != nil || !ed25519.Verify(key, digest[:], signed) {
		return errors.New("archive signature is invalid")
	}
	return nil
}
func hasSkill(skills []SkillRef, name, version string) bool {
	for _, skill := range skills {
		if skill.Name == name && skill.Version == version {
			return true
		}
	}
	return false
}

func sameStrings(left, right []string) bool {
	return strings.Join(canonicalStrings(left), "\x00") == strings.Join(canonicalStrings(right), "\x00")
}
func sortedKeys(values map[string][]byte) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

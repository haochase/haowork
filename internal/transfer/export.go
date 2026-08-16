package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

func ExportBytes(input ExportInput) ([]byte, error) {
	if input.Signer == nil || input.ProvenanceVerifier == nil || strings.TrimSpace(input.Signer.KeyID()) == "" || input.Signer.Algorithm() != SignatureAlgorithm || input.Signer.PublicKeyFingerprint() == "" {
		return nil, errors.New("export signer is required")
	}
	manifest := input.Manifest
	if err := validateManifest(&manifest); err != nil {
		return nil, err
	}
	entries := input.Entries
	manifest.FileSHA256 = make(map[string]string, len(entries))
	for _, entry := range entries {
		if err := validateExportEntry(entry); err != nil {
			return nil, err
		}
		if err := input.ProvenanceVerifier.VerifyProvenance(context.Background(), entry); err != nil {
			return nil, err
		}
		if _, exists := manifest.FileSHA256[entry.Path]; exists {
			return nil, errors.New("duplicate archive path")
		}
		digest := sha256.Sum256(entry.Data)
		manifest.FileSHA256[entry.Path] = hex.EncodeToString(digest[:])
	}
	manifest.ContentSHA256 = contentDigest(manifest.FileSHA256)
	manifest.SignatureAlgorithm = input.Signer.Algorithm()
	manifest.PublicKeyFingerprint = input.Signer.PublicKeyFingerprint()
	canonical, err := canonicalManifest(manifest)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	manifest.SignatureDigest = hex.EncodeToString(digest[:])
	signed, err := input.Signer.Sign(digest[:])
	if err != nil {
		return nil, err
	}
	signature := Signature{KeyID: input.Signer.KeyID(), Algorithm: input.Signer.Algorithm(), Digest: manifest.SignatureDigest, Signature: base64.StdEncoding.EncodeToString(signed)}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	signatureData, err := json.Marshal(signature)
	if err != nil {
		return nil, err
	}
	all := append(entries, Entry{Path: "manifest.json", Data: manifestData}, Entry{Path: "signature.json", Data: signatureData})
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range all {
		header := &zip.FileHeader{Name: entry.Path, Method: zip.Store}
		header.SetModTime(time.Unix(0, 0).UTC())
		header.SetMode(0o600)
		stream, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := stream.Write(entry.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validateExportEntry(entry Entry) error {
	if err := validateArchivePath(entry.Path); err != nil {
		return err
	}
	allowed := map[EntryType]struct{ prefix, source string }{EntryGovernanceEvent: {"governance/", "governance-ledger"}, EntryTrace: {"trace/", "trace-ledger"}, EntryMission: {"mission/", "governance-ledger"}, EntryContext: {"context/", "context-store"}, EntryIdentity: {"identity/", "identity-store"}, EntryGitBaseline: {"git/baseline/", "git"}, EntryGitDiff: {"git/diff/", "git"}, EntryEvidence: {"evidence/", "governance-ledger"}, EntryCheckpoint: {"checkpoints/", "trace-ledger"}, EntryArtifact: {"artifacts/", "artifact-store"}, EntrySkillDefinition: {"skills/", "skill-registry"}}
	rule, known := allowed[entry.Type]
	digest := sha256.Sum256(entry.Data)
	if !known || !strings.HasPrefix(entry.Path, rule.prefix) || len(entry.Data) == 0 || !json.Valid(entry.Data) || entry.Provenance.Source != rule.source || entry.Provenance.SHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("entry type or path is not exportable")
	}
	return nil
}

func validateManifest(manifest *Manifest) error {
	manifest.ProtocolVersion = strings.TrimSpace(manifest.ProtocolVersion)
	manifest.TransferID = strings.TrimSpace(manifest.TransferID)
	manifest.ProjectID = strings.TrimSpace(manifest.ProjectID)
	manifest.SourceEnvironmentID = strings.TrimSpace(manifest.SourceEnvironmentID)
	manifest.TargetEnvironmentID = strings.TrimSpace(manifest.TargetEnvironmentID)
	manifest.ContextID = strings.TrimSpace(manifest.ContextID)
	manifest.ContextHash = strings.TrimSpace(manifest.ContextHash)
	manifest.LeaseID = strings.TrimSpace(manifest.LeaseID)
	manifest.CreatedAt = manifest.CreatedAt.UTC()
	manifest.ExpiresAt = manifest.ExpiresAt.UTC()
	canonicalizeManifest(manifest)
	if manifest.ProtocolVersion != ProtocolVersion || manifest.TransferID == "" || manifest.ProjectID == "" || manifest.SourceEnvironmentID == "" || manifest.TargetEnvironmentID == "" || manifest.SourceEnvironmentID == manifest.TargetEnvironmentID || manifest.GoalVersion < 1 || manifest.ContextID == "" || manifest.ContextHash == "" || manifest.LeaseID == "" || manifest.CreatedAt.IsZero() || manifest.ExpiresAt.IsZero() || !manifest.ExpiresAt.After(manifest.CreatedAt) || len(manifest.Scope) == 0 || len(manifest.Skills) == 0 || len(manifest.Agents) == 0 {
		return errors.New("transfer manifest binding is incomplete")
	}
	for _, skill := range manifest.Skills {
		if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Version) == "" {
			return errors.New("transfer skill identity is incomplete")
		}
	}
	return nil
}

func canonicalizeManifest(manifest *Manifest) {
	manifest.MissionIDs = canonicalStrings(manifest.MissionIDs)
	manifest.GovernanceEventIDs = canonicalStrings(manifest.GovernanceEventIDs)
	manifest.TraceIDs = canonicalStrings(manifest.TraceIDs)
	manifest.TaskIDs = canonicalStrings(manifest.TaskIDs)
	manifest.WorkItemIDs = canonicalStrings(manifest.WorkItemIDs)
	manifest.RunIDs = canonicalStrings(manifest.RunIDs)
	manifest.Scope = canonicalStrings(manifest.Scope)
	sort.Slice(manifest.Skills, func(i, j int) bool {
		if manifest.Skills[i].Name == manifest.Skills[j].Name {
			return manifest.Skills[i].Version < manifest.Skills[j].Version
		}
		return manifest.Skills[i].Name < manifest.Skills[j].Name
	})
	sort.Slice(manifest.Agents, func(i, j int) bool { return manifest.Agents[i].LogicalActorID < manifest.Agents[j].LogicalActorID })
	for index := range manifest.Agents {
		sort.Slice(manifest.Agents[index].Bindings, func(i, j int) bool {
			return manifest.Agents[index].Bindings[i].Revision < manifest.Agents[index].Bindings[j].Revision
		})
	}
	sort.Slice(manifest.MatrixRefs, func(i, j int) bool { return manifest.MatrixRefs[i].EventID < manifest.MatrixRefs[j].EventID })
	sort.Slice(manifest.ArtifactRefs, func(i, j int) bool { return manifest.ArtifactRefs[i].URI < manifest.ArtifactRefs[j].URI })
}

func canonicalStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			if _, exists := seen[value]; !exists {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	sort.Strings(result)
	return result
}

func canonicalManifest(manifest Manifest) ([]byte, error) {
	manifest.FileSHA256 = cloneHashes(manifest.FileSHA256)
	manifest.SignatureDigest = ""
	return json.Marshal(manifest)
}

func contentDigest(hashes map[string]string) string {
	paths := make([]string, 0, len(hashes))
	for name := range hashes {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, name := range paths {
		_, _ = io.WriteString(hash, name+"\x00"+hashes[name]+"\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func cloneHashes(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
func validateArchivePath(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == "manifest.json" || name == "signature.json" {
		return errors.New("archive path is unsafe")
	}
	return nil
}

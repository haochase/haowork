// Package transfer creates and verifies deterministic, signed, portable capsules.
package transfer

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/haochase/haowork/internal/model"
)

const (
	ProtocolVersion    = "1.0"
	SignatureAlgorithm = "Ed25519"
)

type SkillRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type RuntimeHistory struct {
	LogicalActorID string                 `json:"logical_actor_id"`
	Bindings       []model.RuntimeBinding `json:"bindings"`
}
type MatrixRef struct {
	InstanceID  string `json:"instance_id"`
	RoomID      string `json:"room_id"`
	EventID     string `json:"event_id"`
	ContentHash string `json:"content_hash"`
}
type ArtifactRef struct {
	URI    string `json:"uri"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	ProtocolVersion, TransferID, ProjectID, SourceEnvironmentID, TargetEnvironmentID string
	GoalVersion                                                                      int
	MissionIDs, GovernanceEventIDs, TraceIDs, TaskIDs, WorkItemIDs, RunIDs           []string
	ContextID, ContextHash, LeaseID                                                  string
	Scope                                                                            []string
	Skills                                                                           []SkillRef
	Agents                                                                           []RuntimeHistory
	MatrixRefs                                                                       []MatrixRef
	ArtifactRefs                                                                     []ArtifactRef
	TraceCursor, TraceHash, RedactionPolicy, GitBaseline, ParentTransferID           string
	CreatedAt, ExpiresAt                                                             time.Time
	FileSHA256                                                                       map[string]string
	ContentSHA256                                                                    string
	SignatureAlgorithm, PublicKeyFingerprint, SignatureDigest                        string
}

type Signature struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
	Signature string `json:"signature"`
}
type Entry struct {
	Type       EntryType
	Path       string
	Data       []byte
	Provenance EntryProvenance
}
type EntryProvenance struct{ Source, SHA256 string }
type EntryType string

const (
	EntryUnknown         EntryType = "unknown"
	EntryGovernanceEvent EntryType = "governance_event"
	EntryTrace           EntryType = "trace"
	EntryMission         EntryType = "mission"
	EntryContext         EntryType = "context"
	EntryIdentity        EntryType = "identity"
	EntryGitBaseline     EntryType = "git_baseline"
	EntryGitDiff         EntryType = "git_diff"
	EntryEvidence        EntryType = "evidence"
	EntryCheckpoint      EntryType = "checkpoint"
	EntryArtifact        EntryType = "verified_artifact"
	EntrySkillDefinition EntryType = "skill_definition"
	EntryChatTranscript  EntryType = "chat_transcript"
	EntryPrivateMemory   EntryType = "private_memory"
	EntryCredential      EntryType = "credential"
)

type Signer interface {
	KeyID() string
	Algorithm() string
	PublicKeyFingerprint() string
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
func (signer Ed25519Signer) PublicKeyFingerprint() string {
	if len(signer.privateKey) != ed25519.PrivateKeySize {
		return ""
	}
	digest := sha256.Sum256(signer.privateKey.Public().(ed25519.PublicKey))
	return hex.EncodeToString(digest[:])
}
func (signer Ed25519Signer) Sign(digest []byte) ([]byte, error) {
	if signer.keyID == "" || len(signer.privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("ed25519 signer is not configured")
	}
	return ed25519.Sign(signer.privateKey, digest), nil
}

type ExportInput struct {
	Manifest           Manifest
	Entries            []Entry
	Signer             Signer
	ProvenanceVerifier ProvenanceVerifier
}
type Archive struct {
	Bytes     []byte
	KeyID     string
	PublicKey ed25519.PublicKey
}
type RebindCandidate struct {
	LogicalActorID, SourceEnvironmentID, TargetEnvironmentID string
	PreviousRevision, NewRevision                            int
	RuntimePrincipalID, AgentTeamsInstanceID                 string
}
type ImportPreview struct {
	Manifest       Manifest
	RebindRequired []RebindCandidate
	PreviewHash    string
	archive        []byte
}

type ImportWriter interface {
	CommitImport(context.Context, RebindCandidate, string, model.Actor) error
}
type RuntimeBindingResolver interface {
	ResolveRuntimeBinding(context.Context, string, string) (model.RuntimeBinding, error)
}
type RuntimeBindingResolverFunc func(context.Context, string, string) (model.RuntimeBinding, error)

func (fn RuntimeBindingResolverFunc) ResolveRuntimeBinding(ctx context.Context, logicalID, environmentID string) (model.RuntimeBinding, error) {
	return fn(ctx, logicalID, environmentID)
}

type ApprovalVerifier interface {
	VerifyApproval(context.Context, string, string) error
}
type ApprovalVerifierFunc func(context.Context, string, string) error

func (fn ApprovalVerifierFunc) VerifyApproval(ctx context.Context, id, payloadHash string) error {
	return fn(ctx, id, payloadHash)
}

type ProvenanceVerifier interface {
	VerifyProvenance(context.Context, Entry) error
}
type ProvenanceVerifierFunc func(context.Context, Entry) error

func (fn ProvenanceVerifierFunc) VerifyProvenance(ctx context.Context, entry Entry) error {
	return fn(ctx, entry)
}

type ProvenanceVerifierSet map[string]ProvenanceVerifier

func (set ProvenanceVerifierSet) VerifyProvenance(ctx context.Context, entry Entry) error {
	verifier := set[entry.Provenance.Source]
	if verifier == nil {
		return errors.New("entry provenance source is not trusted")
	}
	return verifier.VerifyProvenance(ctx, entry)
}

type BatchEventAppender interface {
	AppendBatch(context.Context, []model.Event) error
}
type CoreTeamWriter struct {
	ProjectID   string
	GoalVersion int
	Appender    BatchEventAppender
	NewEventID  func() string
	Now         func() time.Time
}
type Approval struct {
	PreviewHash string
	Actor       model.Actor
}
type ReturnState struct {
	GoalVersion                           int
	LeaseID                               string
	Scope                                 []string
	GitBaseline, DesignHash, EvidenceHash string
	Terminal                              bool
}
type ApprovedChange struct{ Entry Entry }
type ReturnApproval struct {
	ID            string
	PayloadSHA256 string
}
type ReturnRequest struct {
	Base                Manifest
	Current, Candidate  ReturnState
	Changes             []ApprovedChange
	ApprovedEntryHashes []string
	Approval            ReturnApproval
}
type ReturnDelta struct {
	Manifest  Manifest
	Entries   []Entry
	Conflicts []string
	Archive   []byte
}
type Service struct {
	TargetEnvironmentID    string
	PublicKeys             map[string]ed25519.PublicKey
	ExpectedGoalVersion    int
	ExpectedGitBaseline    string
	ExpectedContextHash    string
	ExpectedLeaseID        string
	ExpectedScope          []string
	RequiredSkills         map[string]string
	Writer                 ImportWriter
	ReturnSigner           Signer
	RuntimeBindingResolver RuntimeBindingResolver
	ApprovalVerifier       ApprovalVerifier
	ProvenanceVerifier     ProvenanceVerifier
	Now                    func() time.Time
}

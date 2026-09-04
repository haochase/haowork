package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/haochase/haowork/internal/model"
)

func (service Service) BuildReturn(ctx context.Context, request ReturnRequest) (ReturnDelta, error) {
	if service.ReturnSigner == nil {
		return ReturnDelta{}, errors.New("return signer is required")
	}
	if service.ApprovalVerifier == nil || request.Approval.ID == "" || request.Approval.PayloadSHA256 != ReturnApprovalHash(request) || service.ApprovalVerifier.VerifyApproval(ctx, request.Approval.ID, request.Approval.PayloadSHA256) != nil {
		return ReturnDelta{}, errors.New("return requires hash-bound human approval")
	}
	approved := make(map[string]struct{}, len(request.ApprovedEntryHashes))
	for _, value := range request.ApprovedEntryHashes {
		approved[value] = struct{}{}
	}
	entries := make([]Entry, 0, len(request.Changes))
	for _, change := range request.Changes {
		if _, ok := approved[EntryApprovalHash(change.Entry)]; ok {
			entries = append(entries, change.Entry)
		}
	}
	if len(entries) == 0 {
		return ReturnDelta{}, errors.New("return requires approved changes")
	}
	manifest := request.Base
	manifest.ParentTransferID = request.Base.TransferID
	manifest.TransferID = request.Base.TransferID + "-return"
	manifest.SourceEnvironmentID, manifest.TargetEnvironmentID = request.Base.TargetEnvironmentID, request.Base.SourceEnvironmentID
	manifest.CreatedAt = time.Now().UTC()
	if service.Now != nil {
		manifest.CreatedAt = service.Now().UTC()
	}
	returnTTL := service.ReturnTTL
	if returnTTL <= 0 {
		returnTTL = time.Hour
	}
	manifest.ExpiresAt = manifest.CreatedAt.Add(returnTTL)
	archive, err := ExportBytes(ExportInput{Manifest: manifest, Entries: entries, Signer: service.ReturnSigner, ProvenanceVerifier: service.ProvenanceVerifier})
	if err != nil {
		return ReturnDelta{}, err
	}
	return ReturnDelta{Manifest: manifest, Entries: entries, Conflicts: detectReturnConflicts(request.Current, request.Candidate), Archive: archive}, nil
}
func EntryApprovalHash(entry Entry) string {
	payload, _ := json.Marshal(entry)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func ReturnApprovalHash(request ReturnRequest) string {
	approved := make(map[string]struct{}, len(request.ApprovedEntryHashes))
	for _, value := range request.ApprovedEntryHashes {
		approved[value] = struct{}{}
	}
	entries := make([]string, 0, len(request.Changes))
	for _, change := range request.Changes {
		if digest := EntryApprovalHash(change.Entry); digest != "" {
			if _, ok := approved[digest]; ok {
				entries = append(entries, digest)
			}
		}
	}
	sort.Strings(entries)
	manifest := request.Base
	canonicalizeManifest(&manifest)
	canonical, _ := canonicalManifest(manifest)
	payload, _ := json.Marshal(struct {
		Manifest json.RawMessage
		Entries  []string
	}{Manifest: canonical, Entries: entries})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func detectReturnConflicts(current, candidate ReturnState) []string {
	conflicts := make([]string, 0, 6)
	if current.GoalVersion != candidate.GoalVersion {
		conflicts = append(conflicts, "stale_goal")
	}
	if current.LeaseID != candidate.LeaseID {
		conflicts = append(conflicts, "lease_reassigned")
	}
	if scopesOverlap(current.Scope, candidate.Scope) {
		conflicts = append(conflicts, "scope_overlap")
	}
	if current.DesignHash != candidate.DesignHash {
		conflicts = append(conflicts, "design_diverged")
	}
	if current.EvidenceHash != candidate.EvidenceHash {
		conflicts = append(conflicts, "evidence_mismatch")
	}
	if current.Terminal {
		conflicts = append(conflicts, "terminal_state")
	}
	return conflicts
}
func scopesOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}

func (writer CoreTeamWriter) CommitImport(ctx context.Context, binding RebindCandidate, previewHash string, actor model.Actor) error {
	if writer.ProjectID == "" || writer.GoalVersion < 1 || writer.Appender == nil || writer.NewEventID == nil {
		return errors.New("core team writer is not configured")
	}
	if binding.RuntimePrincipalID == "" || binding.AgentTeamsInstanceID == "" {
		return errors.New("runtime rebind target is required")
	}
	now := time.Now().UTC()
	if writer.Now != nil {
		now = writer.Now().UTC()
	}
	payload, err := json.Marshal(model.CapsuleImported{PreviewHash: previewHash, Binding: model.RuntimeBinding{LogicalActorID: binding.LogicalActorID, Revision: binding.NewRevision, EnvironmentID: binding.TargetEnvironmentID, AgentTeamsInstanceID: binding.AgentTeamsInstanceID, RuntimePrincipalID: binding.RuntimePrincipalID}})
	if err != nil {
		return err
	}
	bindingPayload, err := json.Marshal(model.RuntimeBound{Binding: model.RuntimeBinding{LogicalActorID: binding.LogicalActorID, Revision: binding.NewRevision, EnvironmentID: binding.TargetEnvironmentID, AgentTeamsInstanceID: binding.AgentTeamsInstanceID, RuntimePrincipalID: binding.RuntimePrincipalID}})
	if err != nil {
		return err
	}
	events := []model.Event{{ID: writer.NewEventID(), Type: "capsule.imported", ProjectID: writer.ProjectID, GoalVersion: writer.GoalVersion, AggregateType: "capsule", AggregateID: previewHash, Actor: actor, OccurredAt: now, Payload: payload}, {ID: writer.NewEventID(), Type: "agent.runtime.bound", ProjectID: writer.ProjectID, GoalVersion: writer.GoalVersion, AggregateType: "agent", AggregateID: binding.LogicalActorID, Actor: actor, OccurredAt: now, Payload: bindingPayload}}
	return writer.Appender.AppendBatch(ctx, events)
}

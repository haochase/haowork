package patchskills

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillruntime"
)

// Adapter intentionally has no filesystem, network, or peer-Core dependency.
type Adapter struct {
	Name                                        string
	Signer                                      Signer
	BuildAgentID, VerifyAgentID                 string
	AuditCommand, WorkspaceDigest, ArtifactHash string
	AuditExitCode                               int
	Now                                         func() time.Time
}

func (adapter Adapter) Invoke(_ context.Context, invocation skillruntime.Invocation) (json.RawMessage, []model.ArtifactRef, error) {
	if adapter.Signer == nil || adapter.Signer.KeyID() == "" || adapter.Signer.Algorithm() != SignatureAlgorithm || adapter.Name != invocation.SkillName {
		return nil, nil, errors.New("cross-zone signer is not configured")
	}
	input := sha256.Sum256(invocation.Input)
	now := time.Now().UTC()
	if adapter.Now != nil {
		now = adapter.Now().UTC()
	}
	function := model.FunctionBuild
	if invocation.LogicalActorID == adapter.VerifyAgentID {
		function = model.FunctionVerify
	}
	request := Request{MissionID: invocation.MissionID, EnvironmentID: invocation.EnvironmentID, SkillName: invocation.SkillName, SkillVersion: invocation.SkillVersion, InputSHA256: hex.EncodeToString(input[:]), LogicalActorID: invocation.LogicalActorID, RuntimeBindingRevision: invocation.RuntimeBindingRevision, AgentFunction: function, Scope: append([]string(nil), invocation.Scope...), BuildAgentID: adapter.BuildAgentID, VerifyAgentID: adapter.VerifyAgentID, CreatedAt: now}
	if adapter.BuildAgentID == "" || adapter.VerifyAgentID == "" {
		return nil, nil, errors.New("cross-zone logical identities are not configured")
	}
	if adapter.Name == "patch" {
		var patch struct {
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(invocation.Input, &patch); err != nil {
			return nil, nil, errors.New("patch request must be JSON")
		}
		if err := ValidatePatchScope(invocation.Scope, patch.Paths); err != nil {
			return nil, nil, err
		}
	}
	if adapter.Name == "audit" {
		if err := ValidateAudit(adapter.BuildAgentID, invocation.LogicalActorID); err != nil {
			return nil, nil, err
		}
		if adapter.AuditCommand == "" || adapter.WorkspaceDigest == "" || adapter.ArtifactHash == "" {
			return nil, nil, errors.New("audit facts are required")
		}
		request.Audit = AuditFacts{Command: adapter.AuditCommand, ExitCode: adapter.AuditExitCode, WorkspaceDigest: adapter.WorkspaceDigest, ArtifactHash: adapter.ArtifactHash}
	}
	canonical, err := canonicalRequest(request)
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(canonical)
	signature, err := adapter.Signer.Sign(digest[:])
	if err != nil {
		return nil, nil, err
	}
	signed := SignedRequest{Request: request, KeyID: adapter.Signer.KeyID(), Algorithm: SignatureAlgorithm, Digest: hex.EncodeToString(digest[:]), Signature: base64.StdEncoding.EncodeToString(signature)}
	output, err := json.Marshal(signed)
	return output, nil, err
}

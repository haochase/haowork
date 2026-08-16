package capsule

import (
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/transfer"
)

const TransferProtocolVersion = transfer.ProtocolVersion

type TransferManifest = transfer.Manifest
type SkillRef = transfer.SkillRef
type RuntimeHistory = transfer.RuntimeHistory
type RuntimeBinding = model.RuntimeBinding
type ArtifactReference = transfer.ArtifactRef
type MatrixReference = transfer.MatrixRef

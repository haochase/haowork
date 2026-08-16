package capsule

import (
	"context"

	"github.com/haochase/haowork/internal/transfer"
)

type TransferService = transfer.Service
type TransferPreview = transfer.ImportPreview
type RebindCandidate = transfer.RebindCandidate
type ImportWriter = transfer.ImportWriter
type TransferApproval = transfer.Approval

func PreviewTransferImport(ctx context.Context, service TransferService, archive []byte) (TransferPreview, error) {
	return service.PreviewImport(ctx, archive)
}

func ApplyTransferImport(ctx context.Context, service TransferService, preview TransferPreview, approval TransferApproval) error {
	return service.ApplyImport(ctx, preview, approval)
}

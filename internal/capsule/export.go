package capsule

import (
	"crypto/ed25519"

	"github.com/haochase/haowork/internal/transfer"
)

type TransferEntry = transfer.Entry
type TransferExportInput = transfer.ExportInput
type TransferSigner = transfer.Signer
type TransferProvenanceVerifier = transfer.ProvenanceVerifier
type TransferProvenanceVerifierFunc = transfer.ProvenanceVerifierFunc

func NewTransferEd25519Signer(keyID string, privateKey ed25519.PrivateKey) transfer.Ed25519Signer {
	return transfer.NewEd25519Signer(keyID, privateKey)
}

// ExportTransfer emits a deterministic signed archive without changing project state.
func ExportTransfer(input TransferExportInput) ([]byte, error) { return transfer.ExportBytes(input) }

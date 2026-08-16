package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/haochase/haowork/internal/core"
)

// TransferConfigProvider resolves environment-specific capabilities for Core.
// Implementations must keep private keys and runtime credentials outside the
// browser-facing API and return nil when transfer is intentionally disabled.
type TransferConfigProvider interface {
	Load(context.Context, string) (*core.TransferConfig, error)
}

// TransferConfigProviderFunc adapts a function to TransferConfigProvider.
type TransferConfigProviderFunc func(context.Context, string) (*core.TransferConfig, error)

func (provider TransferConfigProviderFunc) Load(ctx context.Context, root string) (*core.TransferConfig, error) {
	return provider(ctx, root)
}

// LocalTransferConfigProvider is the standard CLI provider. The local marker
// is deliberately metadata-only: actual signer, resolver and provenance
// capabilities must be injected by a trusted Core host. This prevents the
// CLI from inventing capabilities or accepting browser-held private keys.
type LocalTransferConfigProvider struct{}

const localTransferConfigPath = ".haowork/local/transfer.json"

type localTransferConfig struct {
	Version       int    `json:"version"`
	Target        string `json:"target_environment_id"`
	CapabilityRef string `json:"capability_ref"`
}

func (LocalTransferConfigProvider) Load(ctx context.Context, root string) (*core.TransferConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, localTransferConfigPath)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect local transfer configuration: %w", err)
	}
	if info.IsDir() {
		return nil, errors.New("local transfer configuration is a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("local transfer configuration must be owner-only")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local transfer configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var config localTransferConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, errors.New("local transfer configuration is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("local transfer configuration must contain one JSON object")
	}
	if config.Version != 1 || config.Target == "" || config.CapabilityRef == "" {
		return nil, errors.New("local transfer configuration is incomplete")
	}
	return nil, errors.New("local transfer capabilities require a trusted Core provider")
}

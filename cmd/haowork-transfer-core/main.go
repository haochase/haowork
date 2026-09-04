package main

import (
	"context"
	"io"
	"os"

	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/transferhost"
)

const configEnvironment = "HAOWORK_TRANSFER_CORE_CONFIG"

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, configPath string) int {
	provider := transferhost.FileProvider{Path: configPath}
	commands := append(cli.DefaultCommands(), transferhost.NewKeysCommand, transferhost.NewProtectCommand, transferhost.NewPhysicalCommand, transferhost.NewBootstrapCommand(provider))
	return cli.ExecuteWithDependencies(ctx, args, stdout, stderr, cli.Dependencies{
		Commands:         commands,
		TransferProvider: provider,
	})
}

func main() {
	os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv(configEnvironment)))
}

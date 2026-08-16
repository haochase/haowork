package localcore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

type Browser interface {
	Open(context.Context, string) error
}

type SystemBrowser struct{}

func (SystemBrowser) Open(ctx context.Context, target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", target)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.CommandContext(ctx, "xdg-open", target)
	}
	return command.Start()
}

func OpenBrowser(ctx context.Context, browser Browser, endpoint, bootstrapToken string) error {
	if browser == nil {
		return errors.New("browser is required")
	}
	if strings.TrimSpace(bootstrapToken) == "" {
		return errors.New("bootstrap token is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("parse browser endpoint: %w", err)
	}
	if err := validateLoopbackHTTPURL(parsed); err != nil {
		return err
	}
	parsed.Fragment = "bootstrap=" + url.QueryEscape(bootstrapToken)
	return browser.Open(ctx, parsed.String())
}

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haochase/haowork/internal/higressbinding"
)

const (
	envConsoleURL    = "HAOWORK_HIGRESS_CONSOLE_URL"
	envConsoleUser   = "HAOWORK_HIGRESS_CONSOLE_USERNAME"
	envConsolePass   = "HAOWORK_HIGRESS_CONSOLE_PASSWORD"
	envMCPServer     = "HAOWORK_HIGRESS_MCP_SERVER_NAME"
	envBindingsPath  = "HAOWORK_HIGRESS_BINDINGS_PATH"
	envStatePath     = "HAOWORK_HIGRESS_STATE_PATH"
	envPollInterval  = "HAOWORK_HIGRESS_POLL_INTERVAL"
	envRetryInterval = "HAOWORK_HIGRESS_RETRY_INTERVAL"
	envMaxAttempts   = "HAOWORK_HIGRESS_MAX_ATTEMPTS"
	envAllowedHosts  = "HAOWORK_HIGRESS_ALLOWED_CONSOLE_HOSTS"
	envAllowLoopback = "HAOWORK_HIGRESS_ALLOW_LOOPBACK"
)

type config struct {
	consoleURL, username, password, mcpServerName, bindingsPath, statePath string
	pollInterval, retryInterval                                            time.Duration
	maxAttempts                                                            int
	allowedConsoleHosts                                                    []string
	allowLoopback                                                          bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCommand(ctx, os.Args[1:], os.Getenv); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "haowork-higress-binding-publisher:", err)
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string, getenv func(string) string) error {
	command, statePath, err := parseCommand(args)
	if err != nil {
		return err
	}
	if command == prepareStateDirCommand {
		return prepareStateDirectory(statePath, publisherUID, publisherGID)
	}
	return run(ctx, getenv)
}

func run(ctx context.Context, getenv func(string) string) error {
	configuration, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	console, err := higressbinding.NewHTTPConsole(higressbinding.Config{
		ConsoleURL: configuration.consoleURL, Username: configuration.username, Password: configuration.password,
		MCPServerName: configuration.mcpServerName, AllowedConsoleHosts: configuration.allowedConsoleHosts, AllowLoopback: configuration.allowLoopback,
	}, http.DefaultClient)
	if err != nil {
		return err
	}
	state, err := higressbinding.NewFileManagedState(configuration.statePath)
	if err != nil {
		return err
	}
	publisher := higressbinding.Publisher{Config: higressbinding.Config{
		MCPServerName: configuration.mcpServerName, BindingsPath: configuration.bindingsPath, StatePath: configuration.statePath,
		PollInterval: configuration.pollInterval, RetryInterval: configuration.retryInterval, MaxAttempts: configuration.maxAttempts,
		AllowedConsoleHosts: configuration.allowedConsoleHosts,
	}, Console: console, State: state}
	if err := publisher.Reconcile(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(configuration.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := publisher.Reconcile(ctx); err != nil {
				return err
			}
		}
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	if getenv == nil {
		return config{}, errors.New("environment reader is required")
	}
	result := config{
		consoleURL: strings.TrimSpace(getenv(envConsoleURL)), username: strings.TrimSpace(getenv(envConsoleUser)),
		password: getenv(envConsolePass), mcpServerName: strings.TrimSpace(getenv(envMCPServer)), bindingsPath: strings.TrimSpace(getenv(envBindingsPath)),
		statePath: strings.TrimSpace(getenv(envStatePath)),
	}
	result.pollInterval, _ = time.ParseDuration(strings.TrimSpace(getenv(envPollInterval)))
	result.retryInterval, _ = time.ParseDuration(strings.TrimSpace(getenv(envRetryInterval)))
	result.maxAttempts, _ = strconv.Atoi(strings.TrimSpace(getenv(envMaxAttempts)))
	for _, host := range strings.Split(getenv(envAllowedHosts), ",") {
		if host = strings.TrimSpace(host); host != "" {
			result.allowedConsoleHosts = append(result.allowedConsoleHosts, host)
		}
	}
	result.allowLoopback, _ = strconv.ParseBool(strings.TrimSpace(getenv(envAllowLoopback)))
	if result.consoleURL == "" || result.username == "" || result.password == "" || result.bindingsPath == "" || result.statePath == "" ||
		len(result.allowedConsoleHosts) == 0 || result.mcpServerName != higressbinding.FixedMCPServerName || result.pollInterval <= 0 || result.retryInterval <= 0 || result.maxAttempts < 1 || result.maxAttempts > 10 {
		return config{}, errors.New("Higress Console, fixed MCP server, binding and state paths, polling, and bounded retry settings are required")
	}
	return result, nil
}

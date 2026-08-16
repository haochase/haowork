package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], http.DefaultClient, 5*time.Second); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, client *http.Client, timeout time.Duration) error {
	if len(arguments) == 0 || arguments[0] == "serve" {
		<-ctx.Done()
		return ctx.Err()
	}
	if len(arguments) != 2 || arguments[0] != "probe" {
		return errors.New("usage: haowork-network-probe probe <http-url>")
	}
	parsed, err := url.Parse(strings.TrimSpace(arguments[1]))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("probe target must be a canonical HTTP(S) URL without credentials, query, or fragment")
	}
	if client == nil || timeout <= 0 {
		return errors.New("probe HTTP client and timeout are required")
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe request: %w", err)
	}
	response.Body.Close()
	return fmt.Errorf("unexpected reachable HTTP service: status=%d", response.StatusCode)
}

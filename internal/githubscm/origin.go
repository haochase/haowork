package githubscm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/scm"
)

type Origin struct {
	Owner             string
	Repository        string
	RemoteFingerprint string
}

func DiscoverOrigin(ctx context.Context, runner scm.Runner, root string) (Origin, error) {
	if runner == nil {
		return Origin{}, errors.New("Git runner is required")
	}
	output, err := runner.Run(ctx, root, "remote", "get-url", "--all", "origin")
	if err != nil {
		return Origin{}, fmt.Errorf("read GitHub origin: %w", err)
	}
	var discovered *Origin
	canonical := make([]string, 0)
	for _, raw := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		candidate, parseErr := parseGitHubOrigin(raw)
		if parseErr != nil {
			return Origin{}, parseErr
		}
		if discovered == nil {
			discovered = &candidate
		} else if !strings.EqualFold(discovered.Owner, candidate.Owner) || !strings.EqualFold(discovered.Repository, candidate.Repository) {
			return Origin{}, errors.New("origin contains multiple GitHub repositories")
		}
		canonical = append(canonical, canonicalRemoteIdentity(raw))
	}
	if discovered == nil {
		return Origin{}, errors.New("GitHub origin is required")
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	discovered.RemoteFingerprint = hex.EncodeToString(digest[:])
	return *discovered, nil
}

func parseGitHubOrigin(raw string) (Origin, error) {
	if strings.ContainsAny(raw, "?#") || strings.Contains(raw, "%") {
		return Origin{}, errors.New("GitHub origin cannot contain query, fragment, or escapes")
	}
	var repositoryPath string
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "git@github.com:") {
		repositoryPath = raw[len("git@github.com:"):]
	} else {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Origin{}, errors.New("GitHub origin URL is invalid")
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" {
			return Origin{}, errors.New("GitHub origin must use github.com without query or fragment")
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https":
			if parsed.User != nil {
				return Origin{}, errors.New("HTTPS GitHub origin cannot contain user information")
			}
		case "ssh":
			if parsed.User == nil || parsed.User.Username() != "git" {
				return Origin{}, errors.New("SSH GitHub origin must use the git user")
			}
		default:
			return Origin{}, errors.New("GitHub origin scheme is unsupported")
		}
		repositoryPath = strings.TrimPrefix(parsed.Path, "/")
	}
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 {
		return Origin{}, errors.New("GitHub origin path must contain owner and repository")
	}
	owner := parts[0]
	repository := strings.TrimSuffix(parts[1], ".git")
	if !validGitHubOwner(owner) || !validGitHubRepository(repository) {
		return Origin{}, errors.New("GitHub origin owner or repository is invalid")
	}
	return Origin{Owner: owner, Repository: repository}, nil
}

func validGitHubOwner(value string) bool {
	if len(value) == 0 || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validGitHubRepository(value string) bool {
	if len(value) == 0 || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func canonicalRemoteIdentity(value string) string {
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return strings.TrimSuffix(parsed.String(), "/")
	}
	if at := strings.LastIndex(value, "@"); at >= 0 {
		value = value[at+1:]
	}
	return strings.TrimSuffix(value, "/")
}

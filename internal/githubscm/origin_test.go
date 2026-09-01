package githubscm

import (
	"context"
	"strings"
	"testing"
)

func TestDiscoverOriginAcceptsSupportedGitHubForms(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/haochase/haowork.git\n",
		"ssh://git@github.com/haochase/haowork.git\n",
		"git@github.com:haochase/haowork.git\n",
	} {
		runner := &fakeGitRunner{output: []byte(remote)}
		origin, err := DiscoverOrigin(context.Background(), runner, "E:/repo")
		if err != nil {
			t.Fatalf("DiscoverOrigin(%q): %v", remote, err)
		}
		if origin.Owner != "haochase" || origin.Repository != "haowork" {
			t.Fatalf("origin = %#v", origin)
		}
		if strings.Join(runner.args, " ") != "remote get-url --all origin" {
			t.Fatalf("git args = %#v", runner.args)
		}
	}
}

func TestDiscoverOriginRejectsUnsupportedAndDivergentOrigins(t *testing.T) {
	for _, output := range []string{
		"https://example.com/acme/repo.git\n",
		"https://github.com/acme/repo.git?token=secret\n",
		"https://github.com/acme/group/repo.git\n",
		"git@github.com:acme/repo.git\ngit@github.com:other/repo.git\n",
		"git@github.com:acme%2Frepo.git\n",
	} {
		runner := &fakeGitRunner{output: []byte(output)}
		if _, err := DiscoverOrigin(context.Background(), runner, "E:/repo"); err == nil {
			t.Fatalf("DiscoverOrigin() accepted %q", output)
		}
	}
}

type fakeGitRunner struct {
	output []byte
	err    error
	args   []string
}

func (runner *fakeGitRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	runner.args = append([]string(nil), args...)
	return append([]byte(nil), runner.output...), runner.err
}

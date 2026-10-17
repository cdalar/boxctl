package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadGitHubToken(t *testing.T) {
	prev := ghToken
	t.Cleanup(func() { ghToken = prev })
	ctx := context.Background()

	// BOXCTL_GITHUB_TOKEN first, whatever gh has.
	t.Setenv("BOXCTL_GITHUB_TOKEN", " github_pat_scoped ")
	ghToken = func(context.Context) (string, error) { return "gho_broad", nil }
	if got, err := readGitHubToken(ctx); err != nil || got != "github_pat_scoped" {
		t.Errorf("with the variable set: %q, %v", got, err)
	}

	// Else gh's.
	t.Setenv("BOXCTL_GITHUB_TOKEN", "")
	if got, err := readGitHubToken(ctx); err != nil || got != "gho_broad" {
		t.Errorf("without it: %q, %v", got, err)
	}

	// Neither: say what to do, without a token.
	ghToken = func(context.Context) (string, error) {
		return "", errors.New("gh isn't logged in on this machine (gh auth login)")
	}
	_, err := readGitHubToken(ctx)
	if err == nil || !strings.Contains(err.Error(), "gh auth login") || !strings.Contains(err.Error(), "BOXCTL_GITHUB_TOKEN") || !strings.Contains(err.Error(), "--github off") {
		t.Errorf("neither: %v", err)
	}
}

func TestGitHubRepo(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	if githubRepo(dir) != "" {
		t.Error("no origin should give no repo")
	}
	run(t, dir, "git", "remote", "add", "origin", "https://example.com/placeholder")
	for url, want := range map[string]string{
		"git@github.com:cdalar/boxctl.git":       "cdalar/boxctl",
		"https://github.com/cdalar/boxctl":       "cdalar/boxctl",
		"ssh://git@github.com/cdalar/boxctl.git": "cdalar/boxctl",
		"https://gitlab.com/x/y.git":             "",
	} {
		run(t, dir, "git", "remote", "remove", "origin")
		run(t, dir, "git", "remote", "add", "origin", url)
		if got := githubRepo(dir); got != want {
			t.Errorf("%s: got %q, want %q", url, got, want)
		}
	}
}

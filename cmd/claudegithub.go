package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GitHub credentials for Claude on a box -- docs/plans/claude-on-the-box.md
// in boxctl-vms. Two modes, by --github:
//
//   - store (default): a token is logged in to the box's gh, which also
//     becomes git's credential helper there, so Claude can push and open
//     pull requests whether or not you're attached. The token is
//     BOXCTL_GITHUB_TOKEN if set -- the place for a fine-grained one
//     scoped to the repository -- and otherwise the one gh has here
//     (`gh auth token`). It stays on the box until the box is destroyed.
//   - off: nothing.

const (
	githubStore = "store"
	githubOff   = "off"
)

// ghToken returns the token gh has for github.com here. A variable so
// tests can stand in for gh.
var ghToken = func(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("gh isn't installed on this machine")
		}
		return "", errors.New("gh isn't logged in on this machine (gh auth login)")
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh isn't logged in on this machine (gh auth login)")
	}
	return token, nil
}

// storeGitHubToken logs a token in to gh on the box and makes gh git's
// credential helper there, for --github store.
func storeGitHubToken(ctx context.Context, box *boxSSH) error {
	token, err := readGitHubToken(ctx)
	if err != nil {
		return err
	}
	return loginGitHubToken(ctx, box, token)
}

// readGitHubToken is BOXCTL_GITHUB_TOKEN, or else the token gh has here.
// It is apart from storing it so goal mode can fail before it makes a box.
func readGitHubToken(ctx context.Context) (string, error) {
	if token := strings.TrimSpace(os.Getenv("BOXCTL_GITHUB_TOKEN")); token != "" {
		return token, nil
	}
	token, err := ghToken(ctx)
	if err != nil {
		return "", fmt.Errorf("no GitHub token for the box: %v; set BOXCTL_GITHUB_TOKEN, or --github off to give the box none", err)
	}
	return token, nil
}

// loginGitHubToken logs token in to gh on the box and makes gh git's
// credential helper there.
func loginGitHubToken(ctx context.Context, box *boxSSH, token string) error {
	// /usr/bin/gh, not the wrapper older claude-agent images put first on
	// PATH: it sets GH_TOKEN, and gh refuses to log in over that.
	err := box.run(ctx, `gh=/usr/bin/gh; [ -x "$gh" ] || gh=gh
"$gh" auth login --hostname github.com --with-token && "$gh" auth setup-git --hostname github.com`, strings.NewReader(token+"\n"))
	if err != nil {
		return fmt.Errorf("storing the GitHub token on %s: %w", box.name, err)
	}
	fmt.Fprintf(os.Stderr, "Stored the GitHub token on %s.\n", box.name)
	return nil
}

// githubRepo is the project's GitHub repository as owner/name, from its
// origin, or "".
func githubRepo(dir string) string {
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	for _, prefix := range []string{"git@github.com:", "ssh://git@github.com/", "https://github.com/"} {
		if rest, ok := strings.CutPrefix(url, prefix); ok {
			return strings.TrimSuffix(rest, ".git")
		}
	}
	return ""
}

// copyGitIdentity gives git on the box your user.name and user.email, so
// Claude's commits there are yours and git doesn't refuse to commit.
func copyGitIdentity(ctx context.Context, box *boxSSH, dir string) error {
	var script []string
	for _, key := range []string{"user.name", "user.email"} {
		gitArgs := []string{"config", "--get", key}
		if dir != "" { // "": whatever applies wherever you are
			gitArgs = append([]string{"-C", dir}, gitArgs...)
		}
		out, err := exec.Command("git", gitArgs...).Output()
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(out)); v != "" {
			script = append(script, "git config --global "+key+" "+shellQuote(v))
		}
	}
	if len(script) == 0 {
		return nil
	}
	return box.run(ctx, strings.Join(script, " && "), nil)
}

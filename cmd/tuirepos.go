package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// `:repos`: the GitHub repositories you have access to -- the ones you
// own, collaborate on or reach through an organization, as `gh api
// user/repos` lists them for the account gh is logged in as here --
// newest push first. Enter on one runs `boxctl claude --repo
// owner/name` as a child process, the way `s` runs `boxctl ssh`: a
// fresh box with the repository cloned on a new branch, Claude
// attached, and the box deleted when Claude quits once its work is on
// GitHub. The list is cached in ~/.boxctl/repos.json, since it hardly
// changes and a page of 100 repositories takes gh a few seconds:
// opening the view shows the cache at once, asks GitHub again in the
// background only when the cache is older than repoCacheMaxAge, and R
// always asks. It is never listed on the 5s tick.

// ghRepo is what this keeps of a repository from the GitHub API.
type ghRepo struct {
	FullName      string    `json:"full_name"`
	Description   string    `json:"description"`
	Private       bool      `json:"private"`
	Fork          bool      `json:"fork"`
	Archived      bool      `json:"archived"`
	Language      string    `json:"language"`
	DefaultBranch string    `json:"default_branch"`
	PushedAt      time.Time `json:"pushed_at"`
	HTMLURL       string    `json:"html_url"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

const (
	repoPageSize = 100
	repoMaxPages = 5 // the 500 most recently pushed repositories at most
)

// listGitHubRepos lists the repositories gh's account has access to,
// newest push first, up to repoMaxPages pages. A variable so tests
// stand in for gh.
var listGitHubRepos = func(ctx context.Context) ([]ghRepo, error) {
	var repos []ghRepo
	for page := 1; page <= repoMaxPages; page++ {
		out, err := ghAPI(ctx, fmt.Sprintf("user/repos?sort=pushed&direction=desc&per_page=%d&page=%d", repoPageSize, page))
		if err != nil {
			return nil, err
		}
		got, err := parseRepoPage(out)
		if err != nil {
			return nil, err
		}
		repos = append(repos, got...)
		if len(got) < repoPageSize {
			break
		}
	}
	return repos, nil
}

// repoCacheMaxAge is how old the cached list may be before opening the
// view asks GitHub again (in the background, the cache shown meanwhile).
const repoCacheMaxAge = time.Hour

// repoCache is ~/.boxctl/repos.json.
type repoCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Repos     []ghRepo  `json:"repos"`
}

// repoCachePath is where the list is cached. A variable for tests.
var repoCachePath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".boxctl", "repos.json"), nil
}

// readRepoCache is the cached list and when it was fetched; nil when
// there is none (or it can't be read -- then GitHub is asked).
func readRepoCache() *repoCache {
	p, err := repoCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var c repoCache
	if err := json.Unmarshal(data, &c); err != nil || c.FetchedAt.IsZero() {
		return nil
	}
	return &c
}

// writeRepoCache saves the list just fetched. A failure only means the
// next open asks GitHub again, so it isn't reported.
func writeRepoCache(repos []ghRepo) {
	p, err := repoCachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(repoCache{FetchedAt: time.Now(), Repos: repos})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o600)
}

// listRepos is list("repos"): the cache first, shown at once when
// there is one, then GitHub in the background if the cache is stale or
// missing -- or always, with force (the R key). What GitHub answers
// replaces the cache.
func (m *tuiModel) listRepos(force bool) tea.Cmd {
	if m.loading["repos"] {
		return nil
	}
	if c := readRepoCache(); c != nil && !m.loaded["repos"] {
		m.repos = c.Repos
		m.loaded["repos"] = true
		m.reposAt = c.FetchedAt
	}
	if !force && !m.reposAt.IsZero() && time.Since(m.reposAt) < repoCacheMaxAge {
		return nil
	}
	m.loading["repos"] = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		msg := tuiListMsg{resource: "repos"}
		msg.repos, msg.err = listGitHubRepos(ctx)
		if msg.err == nil {
			writeRepoCache(msg.repos)
		}
		return msg
	}
}

// parseRepoPage reads one page of `gh api user/repos`.
func parseRepoPage(out []byte) ([]ghRepo, error) {
	var repos []ghRepo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("reading gh's answer: %w", err)
	}
	return repos, nil
}

// ghAPI runs `gh api <path>` and returns its output, with gh's own
// reason when it fails: not installed, not logged in, GitHub's error.
func ghAPI(ctx context.Context, path string) ([]byte, error) {
	var stderr strings.Builder
	c := exec.CommandContext(ctx, "gh", "api", path)
	c.Stderr = &stderr
	out, err := c.Output()
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, errors.New("gh isn't installed")
	case ctx.Err() != nil:
		return nil, errors.New("GitHub didn't answer in time")
	case err != nil:
		if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
			return nil, errors.New(line)
		}
		return nil, err
	}
	return out, nil
}

var reposView = &tableView{
	name:     "repos",
	aliases:  []string{"repo", "repositories", "github", "gh"},
	title:    "GitHub Repos",
	resource: "repos",
	onDemand: true,
	columns:  []column{{"REPO", 'R'}, {"PUSHED", 'P'}, {"LANGUAGE", 'L'}, {"VISIBILITY", 'V'}, {"DESCRIPTION", 'D'}},
	stateCol: 3,
	empty:    "No repositories: gh lists the ones its account can reach (gh auth login).",
	hotkeys:  []hotkey{{"enter", "claude on a new box"}, {"d", "describe"}, {"y", "json"}, {"R", "refresh"}},
	rows: func(m *tuiModel) []tableRow {
		var rows []tableRow
		for _, r := range m.repos {
			state := repoVisibility(r)
			if r.Archived {
				state = "archived"
			}
			rows = append(rows, tableRow{
				key:   r.FullName,
				state: state,
				cells: []string{r.FullName, age(r.PushedAt), orDash(r.Language), repoKind(r), r.Description},
				// Newest push first when sorted ascending by PUSHED, like the view's own order.
				sortKeys: []string{"", fmt.Sprintf("%020d", -r.PushedAt.Unix()+1<<62), "", "", ""},
			})
		}
		return rows
	},
}

func repoVisibility(r ghRepo) string {
	if r.Private {
		return "private"
	}
	return "public"
}

// repoKind is the VISIBILITY cell: "private", "public fork",
// "archived private".
func repoKind(r ghRepo) string {
	var parts []string
	if r.Archived {
		parts = append(parts, "archived")
	}
	parts = append(parts, repoVisibility(r))
	if r.Fork {
		parts = append(parts, "fork")
	}
	return strings.Join(parts, " ")
}

// describeRepo is the repos view's `d` page.
func describeRepo(r ghRepo) []string {
	kv := func(k, v string) string { return fmt.Sprintf("%-16s%s", k+":", v) }
	push := "no"
	if r.Permissions.Push {
		push = "yes"
	}
	lines := []string{
		kv("Repo", r.FullName),
		kv("URL", orDash(r.HTMLURL)),
		kv("Description", orDash(r.Description)),
		kv("Visibility", repoKind(r)),
		kv("Default Branch", orDash(r.DefaultBranch)),
		kv("Language", orDash(r.Language)),
		kv("Push Access", push),
	}
	if !r.PushedAt.IsZero() {
		lines = append(lines, kv("Pushed", stamp(r.PushedAt)+" ("+age(r.PushedAt)+" ago)"))
	}
	lines = append(lines, "", "enter starts Claude on a fresh box with it cloned (`"+strings.Join(append([]string{"boxctl"}, claudeRepoArgs(r.FullName)...), " ")+"`).")
	if !r.Permissions.Push {
		lines = append(lines, "Without push access Claude can't push its branch or open a pull request there.")
	}
	return lines
}

// repo finds name among the repositories listed, nil when it isn't there.
func (m *tuiModel) repo(name string) *ghRepo {
	for i := range m.repos {
		if m.repos[i].FullName == name {
			return &m.repos[i]
		}
	}
	return nil
}

// claudeRepoArgs is the command line `enter` runs for repo, less the
// binary.
func claudeRepoArgs(repo string) []string {
	args := []string{"claude", "--repo", repo}
	if apiURLFlag != "" {
		args = append(args, "--api-url", apiURLFlag)
	}
	return args
}

// claudeRepo hands the terminal to `boxctl claude --repo <repo>` (a
// child process, as ssh is: its output -- the progress lines, then
// Claude itself over ssh -- goes to the terminal the alternate screen
// gives back). Its stderr is also kept, the end of it, to say why it
// failed, since the tui's redraw hides what it printed.
func (m tuiModel) claudeRepo(repo string) (tea.Model, tea.Cmd) {
	exe, err := os.Executable()
	if err != nil {
		return m, m.fail("finding boxctl: %v", err)
	}
	cmd := exec.Command(exe, claudeRepoArgs(repo)...)
	tail := &tailBuffer{max: 4096}
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tuiClaudeDoneMsg{repo: repo, err: err, stderr: tail.String()}
	})
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// claudeFailure is why `boxctl claude` failed, from its stderr: the
// line PrintError wrote (the last "Error: ..." line, the way it prints
// on a pipe), else the last line there is, else err itself.
func claudeFailure(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "Error: ") {
			return strings.TrimPrefix(l, "Error: ")
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return err.Error()
}

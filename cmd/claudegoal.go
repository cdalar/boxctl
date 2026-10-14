package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// boxctl claude --repo owner/name [--goal "..."]: no local copy. A new box,
// the repository's default branch cloned from GitHub onto a branch of its
// own (boxctl/<slug>-<id>), and Claude started in it -- attached, or with
// --goal unattended: `claude -p` in a detached tmux session, whose runner
// script opens nothing itself but records how the run ended
// (<goalDir>/status.json: running, done or failed, and the PR's URL) beside
// the log. Claude opens the pull request with gh.
//
// That status is on the box, read by `boxctl claude logs`: boxctl-vms has no
// per-box metadata API to put it in yet.
//
// The run outlives the terminal, so the GitHub token can't be the
// forwarded one, which is lent only while an ssh connection is up: goal
// mode needs --github store.

const boxGoalsDir = "/root/.boxctl/goals"

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// parseRepo splits owner/name, accepting a trailing .git.
func parseRepo(s string) (owner, name string, err error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".git")
	if !repoPattern.MatchString(s) {
		return "", "", fmt.Errorf("--repo %q: use owner/name", s)
	}
	owner, name, _ = strings.Cut(s, "/")
	return owner, name, nil
}

var branchUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// goalSlug is a few words of text as a branch-name part: lowercase letters
// and digits joined by dashes, at most 24 characters.
func goalSlug(text string) string {
	slug := strings.Trim(branchUnsafe.ReplaceAllString(strings.ToLower(text), "-"), "-")
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
	}
	if slug == "" {
		slug = "work"
	}
	return slug
}

// goalBranch is the new branch's name.
func goalBranch(text, id string) string { return "boxctl/" + goalSlug(text) + "-" + id }

func newGoalID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// checkRepoFlags checks what --repo/--goal can't be combined with, before
// anything is done. conflicts names the other flags that were given.
func checkRepoFlags(goal, github string, conflicts []string) error {
	if len(conflicts) > 0 {
		return fmt.Errorf("--repo/--goal can't be combined with %s", strings.Join(conflicts, ", "))
	}
	if goal != "" && github != githubStore {
		return fmt.Errorf(`--goal needs --github store, not %s: the run goes on after you close this terminal, and %s only works while you're attached.
Give the box a token it keeps: set BOXCTL_GITHUB_TOKEN to a fine-grained token for just this repository (Contents and Pull requests read/write, with an expiry; https://github.com/settings/personal-access-tokens/new), or run from a terminal and paste it when asked, then add --github store`, github, github)
	}
	return nil
}

// goalPrompt is what Claude is told: the goal, then how the run ends.
func goalPrompt(goal, repo, branch string) string {
	return goal + `

---
You're running unattended on a box, in a clone of ` + repo + ` on the new branch ` + branch + `; nobody can answer questions, so make reasonable decisions and say what you assumed. When the goal is done: commit your work, push the branch (git push -u origin ` + branch + `), and open a pull request with gh pr create. Its body must contain the goal above, word for word, under "## Goal", and a summary of what you changed and anything left undone under "## Summary". If you can't get anywhere, don't open a pull request: say why in your final answer.`
}

// goalRun is one unattended run.
type goalRun struct {
	dir, branch string
	goalDir     string   // where its prompt, log and status live
	envFile     string   // secrets to source and delete first
	args        []string // extra arguments for claude
	gh          string   // gh to ask about the pull request; "" for the box's
}

// runnerScript is the shell script tmux runs: pick up the credentials, run
// claude -p to the end, then find the pull request on the branch and write
// down how it went.
func (g goalRun) runnerScript() string {
	gh := `gh=/usr/bin/gh; [ -x "$gh" ] || gh=gh`
	if g.gh != "" {
		gh = "gh=" + shellQuote(g.gh)
	}
	var extra []string
	for _, a := range g.args {
		extra = append(extra, shellQuote(a))
	}
	return `#!/bin/sh
f=` + shellQuote(g.envFile) + `
if [ -f "$f" ]; then . "$f"; rm -f "$f"; fi
PATH="$HOME/.local/bin:$PATH"; export PATH
# Claude refuses --dangerously-skip-permissions as root unless it's told it's in a sandbox -- which a box is.
IS_SANDBOX=1; export IS_SANDBOX
d=` + shellQuote(g.goalDir) + `
` + gh + `
status() { # status pr_url error
  printf '{"status":"%s","branch":"%s","pr_url":"%s","error":"%s","updated":"%s"}\n' "$1" ` + shellQuote(g.branch) + ` "$2" "$3" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$d/status.json.tmp" && mv "$d/status.json.tmp" "$d/status.json"
}
cd ` + shellQuote(g.dir) + ` || { status failed "" "no project directory"; exit 1; }
status running "" ""
claude -p "$(cat "$d/prompt.txt")" --dangerously-skip-permissions --output-format stream-json --verbose ` + strings.Join(extra, " ") + ` >>"$d/claude.log" 2>&1
rc=$?
pr=$("$gh" pr list --head ` + shellQuote(g.branch) + ` --state all --json url --jq '.[0].url' 2>/dev/null)
if [ "$rc" -ne 0 ]; then status failed "$pr" "claude exited with status $rc"
elif [ -z "$pr" ]; then status failed "" "claude finished without opening a pull request"
else status done "$pr" ""
fi
`
}

// cloneScript clones repo into dir and branches. It refuses a directory
// that's there: a --box that holds something already.
func cloneScript(repo, dir, branch string) string {
	return `set -e
[ ! -e ` + shellQuote(dir) + ` ] || { echo "` + dir + ` already exists on this box" >&2; exit 1; }
git clone -q https://github.com/` + repo + `.git ` + shellQuote(dir) + `
git -C ` + shellQuote(dir) + ` checkout -q -b ` + shellQuote(branch)
}

// repoToUse is --repo, or this directory's GitHub repository.
func repoToUse() (string, error) {
	if claudeRepo != "" {
		owner, name, err := parseRepo(claudeRepo)
		if err != nil {
			return "", err
		}
		return owner + "/" + name, nil
	}
	dir, err := projectDir()
	if err != nil {
		return "", err
	}
	if repo := githubRepo(dir); repo != "" {
		return repo, nil
	}
	return "", fmt.Errorf("--goal needs a repository: pass --repo owner/name, or run it in a git repository whose origin is on GitHub (%s isn't)", dir)
}

func runRepo(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if claudeRepo == "" && claudeGoal == "" {
		return errors.New("nothing to do")
	}
	var conflicts []string
	for flag, set := range map[string]bool{
		"--task": claudeTask != "", "--own-box": claudeOwnBox, "--handoff": claudeHandoff != "",
		"--prompt": claudePrompt != "", "--project": claudeProject != "" && claudeRepo != "",
	} {
		if set {
			conflicts = append(conflicts, flag)
		}
	}
	if claudeGoal != "" {
		if claudeDetach {
			conflicts = append(conflicts, "--detach (a goal always runs detached)")
		}
	}
	if err := checkRepoFlags(claudeGoal, claudeGitHub, conflicts); err != nil {
		return err
	}
	switch claudeGitHub {
	case githubForward, githubStore, githubOff:
	default:
		return fmt.Errorf("--github must be %s, %s or %s", githubForward, githubStore, githubOff)
	}
	repo, err := repoToUse()
	if err != nil {
		return err
	}
	_, repoName, _ := strings.Cut(repo, "/")

	// Everything that can fail fails before a box is made.
	env, apiKey, err := claudeEnv(claudeAuth)
	if err != nil {
		return err
	}
	if claudeGoal != "" && env == "" {
		return errors.New("a goal runs without you, so Claude can't log in on the box with /login: run boxctl claude login once, or use --claude-auth api-key")
	}
	id := newGoalID()
	name := claudeBox
	if name == "" {
		name = claudeBoxName("/"+repoName) + "-" + id
	}
	token := ""
	if claudeGitHub == githubStore {
		if token, err = readGitHubToken(name, repo); err != nil {
			return err
		}
	}
	text := claudeGoal
	if text == "" {
		text = repoName
	}
	branch := goalBranch(text, id)
	dir := path.Join("/root", repoName)

	box, err := connectClaudeBox(ctx, name, true, cmd.Flags().Changed("idle-ttl"))
	if err != nil {
		return err
	}
	defer box.close()

	if token != "" {
		if err := loginGitHubToken(ctx, box, token); err != nil {
			return err
		}
	}
	if err := copyGitIdentity(ctx, box, ""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't set your git identity on %s: %v\n", name, err)
	}
	fmt.Fprintf(os.Stderr, "Cloning %s on %s...\n", repo, name)
	clone := cloneScript(repo, dir, branch)
	if claudeGitHub == githubForward {
		err = box.runForwarded(ctx, clone, boxGitHubSocket)
	} else {
		err = box.run(ctx, clone, nil)
	}
	if err != nil {
		return fmt.Errorf("cloning %s on %s: %w", repo, name, err)
	}
	if err := markProject(ctx, box, dir); err != nil {
		return err
	}
	if err := copyClaudeConfig(ctx, box); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't copy your Claude configuration: %v\n", err)
	}
	if err := box.run(ctx, agent.setupScript(), nil); err != nil {
		return fmt.Errorf("installing Claude Code on %s: %w", name, err)
	}
	if err := box.run(ctx, claudeStateScript([]string{dir}, localClaudeTheme(), apiKey), nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't skip Claude's first-run setup on %s: %v\n", name, err)
	}

	sess := taskSession{dir: dir, tmux: agent.name, ghSocket: boxGitHubSocket}
	if claudeGoal == "" {
		if err := stageClaudeEnv(ctx, box, sess, env); err != nil {
			return fmt.Errorf("passing Claude its credentials: %w", err)
		}
		fmt.Fprintf(os.Stderr, "On branch %s. Come back to Claude with: boxctl ssh %s, then tmux attach -t %s\n", branch, name, sess.tmux)
		return startSession(ctx, &claudeTarget{dir: dir, name: name, box: box}, sess, false, args)
	}

	sess.tmux = agent.name + "-goal-" + id
	g := goalRun{dir: dir, branch: branch, goalDir: boxGoalsDir + "/" + id, envFile: sess.envFile(), args: args}
	if err := stageClaudeEnv(ctx, box, sess, env); err != nil {
		return fmt.Errorf("passing Claude its credentials: %w", err)
	}
	if err := box.run(ctx, "umask 077 && mkdir -p "+shellQuote(g.goalDir)+" && cat >"+shellQuote(g.goalDir+"/prompt.txt"), strings.NewReader(goalPrompt(claudeGoal, repo, branch))); err != nil {
		return fmt.Errorf("writing the goal on %s: %w", name, err)
	}
	if err := box.run(ctx, "cat >"+shellQuote(g.goalDir+"/run.sh")+" && ln -sfn "+shellQuote(g.goalDir)+" "+boxGoalsDir+"/latest", strings.NewReader(g.runnerScript())); err != nil {
		return fmt.Errorf("writing the runner on %s: %w", name, err)
	}
	if err := box.run(ctx, sess.tmuxCommand("sh "+shellQuote(g.goalDir+"/run.sh"), true), nil); err != nil {
		return fmt.Errorf("starting Claude on %s: %w", name, err)
	}
	fmt.Printf("Box:    %s\nBranch: %s\nLogs:   boxctl claude logs %s [-f]\n", name, branch, name)
	return nil
}

// --- boxctl claude logs ---------------------------------------------------

var claudeLogsFollow bool

var claudeLogsCmd = &cobra.Command{
	Use:   "logs <box>",
	Short: "Show how a --goal run on a box is going",
	Long: `Prints the status of the box's latest --goal run (running, done or failed,
and the pull request's URL), then its log: Claude's output as JSON lines.
-f keeps following it.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeBoxName,
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		return goalLogs(cmd.Context(), args[0], claudeLogsFollow)
	}),
}

func init() {
	claudeLogsCmd.Flags().BoolVarP(&claudeLogsFollow, "follow", "f", false, "keep following the log")
	claudeCmd.AddCommand(claudeLogsCmd)
}

func goalLogs(ctx context.Context, name string, follow bool) error {
	box, err := connectClaudeBox(ctx, name, false, false)
	if err != nil {
		return err
	}
	defer box.close()
	return box.stream(ctx, logsScript(follow))
}

func logsScript(follow bool) string {
	tail := "tail -n 200"
	if follow {
		tail = "tail -n 200 -f"
	}
	d := boxGoalsDir + "/latest"
	return `[ -d ` + d + ` ] || { echo "no --goal run on this box" >&2; exit 1; }
cat ` + d + `/status.json 2>/dev/null
exec ` + tail + ` ` + d + `/claude.log`
}

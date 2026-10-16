package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
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
// --repo can be given more than once (or comma-separated). The clones then
// sit side by side under boxWorkspace, Claude starts in that parent
// directory, every repository gets the same new branch, and a goal ends in
// one pull request per repository Claude changed.
//
// That status is on the box, read by `boxctl claude logs` and `ls`
// (claudegoalstatus.go): boxctl-vms has no per-box metadata API to put it
// in yet.
//
// The run outlives the terminal, so the GitHub token can't be the
// forwarded one, which is lent only while an ssh connection is up: goal
// mode needs --github store.

const boxGoalsDir = "/root/.boxctl/goals"

// boxWorkspace is the parent directory of the clones when there is more than
// one repository; a single one is cloned straight to /root/<name>.
const boxWorkspace = "/root/workspace"

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

// parseRepos parses every --repo value, dropping repeats. Two repositories
// with the same name (a/x, b/x) would be cloned to the same directory, so
// that is refused up front rather than halfway through cloning.
func parseRepos(specs []string) ([]string, error) {
	var repos []string
	owners := map[string]string{} // name -> owner
	for _, spec := range specs {
		owner, name, err := parseRepo(spec)
		if err != nil {
			return nil, err
		}
		if prev, ok := owners[name]; ok {
			if prev == owner {
				continue
			}
			return nil, fmt.Errorf("--repo: %s/%s and %s/%s would both be cloned to a directory called %s", prev, name, owner, name, name)
		}
		owners[name] = owner
		repos = append(repos, owner+"/"+name)
	}
	return repos, nil
}

// repoDirs is where each repository is cloned on the box, and the directory
// Claude starts in: the one clone itself, or the workspace holding them all.
func repoDirs(repos []string) (dirs []string, workDir string) {
	for _, r := range repos {
		_, name, _ := strings.Cut(r, "/")
		if len(repos) == 1 {
			dirs = append(dirs, path.Join("/root", name))
		} else {
			dirs = append(dirs, path.Join(boxWorkspace, name))
		}
	}
	if len(repos) == 1 {
		return dirs, dirs[0]
	}
	return dirs, boxWorkspace
}

var branchUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// checkRepoFlags checks what --repo/--goal can't be combined with, before
// anything is done. conflicts names the other flags that were given.
func checkRepoFlags(goal, github string, conflicts []string) error {
	if len(conflicts) > 0 {
		return fmt.Errorf("--repo/--goal can't be combined with %s", strings.Join(conflicts, ", "))
	}
	if goal != "" && github != githubStore {
		return fmt.Errorf(`--goal needs --github store, not %s: the run goes on after you close this terminal, and %s only works while you're attached.
Give the box a token it keeps: set BOXCTL_GITHUB_TOKEN to a fine-grained token for just the repository (or repositories) involved (Contents and Pull requests read/write, with an expiry; https://github.com/settings/personal-access-tokens/new), or run from a terminal and paste it when asked, then add --github store`, github, github)
	}
	return nil
}

// issueGoal is the goal for resolving issue number of repo, plus any extra
// instructions. Claude reads the issue itself, with the token the box has.
func issueGoal(repo string, number int, extra string) string {
	g := fmt.Sprintf("Resolve GitHub issue #%d of %s. Read it, and its comments, with gh issue view %d --repo %s --comments; implement what it asks for, and put \"Closes #%d\" in the pull request's body.", number, repo, number, repo, number)
	if extra = strings.TrimSpace(extra); extra != "" {
		g += "\n\n" + extra
	}
	return g
}

// goalPrompt is what Claude is told: the goal, then how the run ends. dirs
// are the clones of repos (parallel slices); with more than one, Claude is in
// the directory above them.
func goalPrompt(goal string, repos, dirs []string, branch string) string {
	if len(repos) == 1 {
		return goal + `

---
You're running unattended on a box, in a clone of ` + repos[0] + ` on the new branch ` + branch + `; nobody can answer questions, so make reasonable decisions and say what you assumed. When the goal is done: commit your work, push the branch (git push -u origin ` + branch + `), and open a pull request with gh pr create. Its body must contain the goal above, word for word, under "## Goal", and a summary of what you changed and anything left undone under "## Summary". If you can't get anywhere, don't open a pull request: say why in your final answer.`
	}
	var list []string
	for i, r := range repos {
		list = append(list, "- "+r+", in "+dirs[i])
	}
	return goal + `

---
You're running unattended on a box, in a directory that holds a clone of each of these repositories, every one on the new branch ` + branch + `:
` + strings.Join(list, "\n") + `

Nobody can answer questions, so make reasonable decisions and say what you assumed. Change only the repositories the goal needs. When the goal is done, for each repository you changed: commit your work, push the branch (git -C <its directory> push -u origin ` + branch + `), and open a pull request there with gh pr create, run inside that repository's directory. Each pull request's body must contain the goal above, word for word, under "## Goal", and under "## Summary" what you changed in that repository and anything left undone; when the change spans repositories, also link the other pull requests there. Leave a repository you didn't change alone: no commit, no pull request. If you can't get anywhere, don't open any pull request: say why in your final answer.`
}

// goalRun is one unattended run.
type goalRun struct {
	dir, branch string
	repoDirs    []string // the clones to look for pull requests in; dir itself if empty
	goalDir     string   // where its prompt, log and status live
	envFile     string   // secrets to source and delete first
	args        []string // extra arguments for claude
	gh          string   // gh to ask about the pull request; "" for the box's
}

// runnerScript is the shell script tmux runs: pick up the credentials, run
// claude -p to the end, then find the pull request on the branch in each
// repository and write down how it went. status.json's pr_url is the first
// pull request, pr_urls all of them.
func (g goalRun) runnerScript() string {
	gh := `gh=/usr/bin/gh; [ -x "$gh" ] || gh=gh`
	if g.gh != "" {
		gh = "gh=" + shellQuote(g.gh)
	}
	repoDirs := g.repoDirs
	if len(repoDirs) == 0 {
		repoDirs = []string{g.dir}
	}
	var quotedDirs []string
	for _, d := range repoDirs {
		quotedDirs = append(quotedDirs, shellQuote(d))
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
first=""; urls=""
status() { # status error
  printf '{"status":"%s","branch":"%s","pr_url":"%s","pr_urls":[%s],"error":"%s","updated":"%s"}\n' "$1" ` + shellQuote(g.branch) + ` "$first" "$urls" "$2" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$d/status.json.tmp" && mv "$d/status.json.tmp" "$d/status.json"
  # And to the control plane, over the box's vsock device, if the image
  # can (boxctl-vms's images/claude-agent/boxctl-status): best effort, in
  # the background, outliving this script (setsid: tmux hangs up on its
  # session when the run ends). It re-reads the file under a lock, so the
  # last report through is the newest.
  if command -v boxctl-status >/dev/null; then setsid boxctl-status "$d/status.json" >>"$d/status-report.log" 2>&1 </dev/null & fi
}
cd ` + shellQuote(g.dir) + ` || { status failed "no project directory"; exit 1; }
status running ""
claude -p "$(cat "$d/prompt.txt")" --dangerously-skip-permissions --output-format stream-json --verbose ` + strings.Join(extra, " ") + ` >>"$d/claude.log" 2>&1
rc=$?
for r in ` + strings.Join(quotedDirs, " ") + `; do
  u=$(cd "$r" 2>/dev/null && "$gh" pr list --head ` + shellQuote(g.branch) + ` --state all --json url --jq '.[0].url' 2>/dev/null)
  if [ -n "$u" ]; then [ -n "$first" ] || first=$u; urls="$urls${urls:+,}\"$u\""; fi
done
if [ "$rc" -ne 0 ]; then status failed "claude exited with status $rc"
elif [ -z "$first" ]; then status failed "claude finished without opening a pull request"
else status done ""
fi
`
}

// cloneScript clones url's default branch into dir and branches. It
// refuses a directory that's there: a --box that holds something already.
func cloneScript(url, dir, branch string) string {
	return `set -e
[ ! -e ` + shellQuote(dir) + ` ] || { echo "` + dir + ` already exists on this box" >&2; exit 1; }
mkdir -p "$(dirname ` + shellQuote(dir) + `)"
git clone -q --single-branch ` + shellQuote(url) + ` ` + shellQuote(dir) + `
git -C ` + shellQuote(dir) + ` checkout -q -b ` + shellQuote(branch)
}

// reposToUse is the --repo values, or this directory's GitHub repository.
func reposToUse() ([]string, error) {
	if len(claudeRepos) > 0 {
		return parseRepos(claudeRepos)
	}
	dir, err := projectDir()
	if err != nil {
		return nil, err
	}
	if repo := githubRepo(dir); repo != "" {
		return []string{repo}, nil
	}
	return nil, fmt.Errorf("--goal/--issue needs a repository: pass --repo owner/name, or run it in a git repository whose origin is on GitHub (%s isn't)", dir)
}

// newBoxLabels, when set, are the labels ensureClaudeBox gives a box it
// creates: runRepo's, for its run box.
var newBoxLabels map[string]string

// runLabels mark a --repo or --goal run's box for the server
// (boxctl-vms's labels), so claude ls finds it -- and the repository,
// branch and issue it's for -- without guessing from its name or
// reaching into it.
func runLabels(isGoal bool, repos []string, branch string, issue int) map[string]string {
	run := "repo"
	if isGoal {
		run = "goal"
	}
	labels := map[string]string{"boxctl.run": run, "boxctl.agent": agent.name, "boxctl.branch": branch}
	// A label value is at most 255 characters; a workspace of that many
	// repositories goes without.
	if r := strings.Join(repos, ","); len(r) <= 255 {
		labels["boxctl.repo"] = r
	}
	if issue > 0 {
		labels["boxctl.issue"] = strconv.Itoa(issue)
	}
	return labels
}

func runRepo(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if len(claudeRepos) == 0 && claudeGoal == "" && claudeIssue == 0 {
		return errors.New("nothing to do")
	}
	if claudeIssue < 0 {
		return fmt.Errorf("--issue %d: use an issue number", claudeIssue)
	}
	var conflicts []string
	for flag, set := range map[string]bool{
		"--task": claudeTask != "", "--own-box": claudeOwnBox, "--handoff": claudeHandoff != "",
		"--prompt": claudePrompt != "", "--project": claudeProject != "" && len(claudeRepos) > 0,
	} {
		if set {
			conflicts = append(conflicts, flag)
		}
	}
	if claudeGoal != "" || claudeIssue != 0 {
		if claudeDetach {
			conflicts = append(conflicts, "--detach (a goal always runs detached)")
		}
	}
	repos, err := reposToUse()
	if err != nil {
		return err
	}
	// goal is what Claude is asked to do unattended; "" for an attended run.
	goal, slugText := claudeGoal, claudeGoal
	if claudeIssue != 0 {
		if len(repos) != 1 {
			return errors.New("--issue is an issue of one repository: pass a single --repo")
		}
		goal = issueGoal(repos[0], claudeIssue, claudeGoal)
		slugText = fmt.Sprintf("issue %d", claudeIssue)
	}
	if err := checkRepoFlags(goal, claudeGitHub, conflicts); err != nil {
		return err
	}
	switch claudeGitHub {
	case githubForward, githubStore, githubOff:
	default:
		return fmt.Errorf("--github must be %s, %s or %s", githubForward, githubStore, githubOff)
	}
	dirs, workDir := repoDirs(repos)
	// A box with several repositories is named for the workspace, not for one of them.
	namePrefix := "workspace"
	if len(repos) == 1 {
		_, namePrefix, _ = strings.Cut(repos[0], "/")
	}
	// A goal's box says so, so claude ls finds it from anywhere.
	if goal != "" {
		namePrefix = goalBoxPrefix + namePrefix
	}

	// Everything that can fail fails before a box is made.
	env, apiKey, err := claudeEnv(claudeAuth)
	if err != nil {
		return err
	}
	if goal != "" && env == "" {
		return errors.New("a goal runs without you, so Claude can't log in on the box with /login: run boxctl claude login once, or use --claude-auth api-key")
	}
	// A goal's words name the box and branch; without one, an
	// adjective-noun pair does. --box names the box, not the branch.
	gen, err := newBoxName(ctx, namePrefix, slugText)
	if err != nil {
		return err
	}
	id, name := gen.id, gen.box()
	if claudeBox != "" {
		name = claudeBox
	}
	token := ""
	if claudeGitHub == githubStore {
		if token, err = readGitHubToken(name, strings.Join(repos, ", ")); err != nil {
			return err
		}
	}
	branch := gen.branch()
	newBoxLabels = runLabels(goal != "", repos, branch, claudeIssue)

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
	for i, repo := range repos {
		fmt.Fprintf(os.Stderr, "Cloning %s on %s...\n", repo, name)
		clone := cloneScript("https://github.com/"+repo+".git", dirs[i], branch)
		if claudeGitHub == githubForward {
			err = box.runForwarded(ctx, clone, boxGitHubSocket)
		} else {
			err = box.run(ctx, clone, nil)
		}
		if err != nil {
			return fmt.Errorf("cloning %s on %s: %w", repo, name, err)
		}
	}
	if err := markProject(ctx, box, workDir); err != nil {
		return err
	}
	if err := copyClaudeConfig(ctx, box); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't copy your Claude configuration: %v\n", err)
	}
	if err := box.run(ctx, agent.setupScript(), nil); err != nil {
		return fmt.Errorf("installing Claude Code on %s: %w", name, err)
	}
	if err := box.run(ctx, claudeStateScript(append([]string{workDir}, dirs...), localClaudeTheme(), apiKey), nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't skip Claude's first-run setup on %s: %v\n", name, err)
	}

	sess := taskSession{dir: workDir, tmux: agent.name, ghSocket: boxGitHubSocket}
	if goal == "" {
		if err := stageClaudeEnv(ctx, box, sess, env); err != nil {
			return fmt.Errorf("passing Claude its credentials: %w", err)
		}
		fmt.Fprintf(os.Stderr, "On branch %s. Come back to Claude with: boxctl claude --box %s\n", branch, name)
		return startSession(ctx, &claudeTarget{dir: workDir, name: name, box: box}, sess, false, args)
	}

	sess.tmux = agent.name + "-goal-" + id
	g := goalRun{dir: workDir, repoDirs: dirs, branch: branch, goalDir: boxGoalsDir + "/" + id, envFile: sess.envFile(), args: args}
	if err := stageClaudeEnv(ctx, box, sess, env); err != nil {
		return fmt.Errorf("passing Claude its credentials: %w", err)
	}
	if err := box.run(ctx, "umask 077 && mkdir -p "+shellQuote(g.goalDir)+" && cat >"+shellQuote(g.goalDir+"/prompt.txt"), strings.NewReader(goalPrompt(goal, repos, dirs, branch))); err != nil {
		return fmt.Errorf("writing the goal on %s: %w", name, err)
	}
	if err := box.run(ctx, "cat >"+shellQuote(g.goalDir+"/run.sh")+" && ln -sfn "+shellQuote(g.goalDir)+" "+boxGoalsDir+"/latest", strings.NewReader(g.runnerScript())); err != nil {
		return fmt.Errorf("writing the runner on %s: %w", name, err)
	}
	if err := box.run(ctx, sess.tmuxCommand("sh "+shellQuote(g.goalDir+"/run.sh"), true), nil); err != nil {
		return fmt.Errorf("starting Claude on %s: %w", name, err)
	}
	if len(repos) > 1 {
		fmt.Printf("Repos:  %s\n", strings.Join(repos, ", "))
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
-f keeps following it until the run ends. A finished run's box is paused
afterwards, if it was paused before or you followed it to the end.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeBoxName,
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		return goalLogs(cmd.Context(), args[0], claudeLogsFollow)
	}),
}

func init() {
	claudeLogsCmd.Flags().BoolVarP(&claudeLogsFollow, "follow", "f", false, "keep following the log until the run ends")
	claudeCmd.AddCommand(claudeLogsCmd)
}

func goalLogs(ctx context.Context, name string, follow bool) error {
	c := newClient()
	vm, err := findVM(ctx, c, name)
	if err != nil {
		return err
	}
	box, err := connectClaudeBox(ctx, name, false, false)
	if err != nil {
		return err
	}
	defer box.close()
	if err := box.stream(ctx, logsScript(follow)); err != nil {
		return err
	}
	st, err := readGoalStatus(ctx, box)
	if err != nil || !st.finished() {
		return err
	}
	rememberGoals(map[string]goalStatus{name: *st}, nil)
	// Followed to the end, or resumed just to read it: nobody else is
	// using the box, so it needn't run on.
	unused := follow || vm.State != "running"
	_, err = pauseFinishedGoal(ctx, c, vm, st, unused)
	return err
}

// logsScript prints the latest goal's status and log. Following, it
// stops once the run has ended, with the status it ended with.
func logsScript(follow bool) string {
	d := boxGoalsDir + "/latest"
	s := `[ -d ` + d + ` ] || { echo "no --goal run on this box" >&2; exit 1; }
cat ` + d + `/status.json 2>/dev/null
`
	if !follow {
		return s + `exec tail -n 200 ` + d + `/claude.log`
	}
	return s + `tail -n 200 -f ` + d + `/claude.log & t=$!
while [ ! -f ` + d + `/status.json ] || grep -q '"status":"running"' ` + d + `/status.json; do sleep 5; done
sleep 1; kill $t 2>/dev/null; wait $t 2>/dev/null
echo; cat ` + d + `/status.json`
}

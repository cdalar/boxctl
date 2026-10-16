package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

// Running Claude Code itself on a box, with the project there -- see
// boxctl-vms's docs/plans/claude-on-the-box.md. Unlike the Claude Code
// plugin, which keeps Claude here and sends only its Bash commands to a
// box, this moves Claude: its file tools and its shell are both on the
// box, so the project is copied there once and never synced.
//
// Everything after the box is up goes over real ssh to the box's sshd,
// tunneled by `boxctl ssh-proxy` as ssh's ProxyCommand: copying the
// project needs a byte stream into the box, and attaching needs a pty
// plus a command, neither of which boxctl's own terminal protocol
// carries.

const (
	claudeDefaultImage = "claude-agent"
	claudeDefaultSize  = "ai-sandbox" // every agent's: 2 vCPU, 4 GiB, 10 GiB disk
	// claudeInstall is Anthropic's installer, run on first use rather than
	// baking Claude Code into the image (which would redistribute it). It
	// installs to ~/.local/bin and keeps itself updated.
	claudeInstall = "curl -fsSL https://claude.ai/install.sh | bash"
	// claudeReadyTimeout bounds waiting for a new or resumed box to boot.
	claudeReadyTimeout = 3 * time.Minute
	// claudeDefaultIdleTTL is the idle TTL of a box created for Claude.
	// The reaper counts relayed traffic as use, and a detached Claude
	// working on its own makes none, so the server's 6h would pause it
	// mid-task.
	claudeDefaultIdleTTL = 24 * time.Hour
	// projectMarker on the box records which project it holds, so a box
	// is never handed a second one by running boxctl claude --box from
	// another directory.
	projectMarker = "/root/.boxctl/project"
)

var (
	claudeBox     string
	claudeSize    string
	claudeImage   string
	claudePrompt  string
	claudeBranch  string // set with a generated box name: the branch it goes with
	claudeDetach  bool
	claudeGitHub  string
	claudeIdle    time.Duration
	claudeProject string
	claudeHandoff string
	claudeTask    string
	claudeOwnBox  bool
	claudeAuth    string
	claudeRepos   []string
	claudeGoal    string
	claudeIssue   int
	claudeKeep   bool
)

var claudeCmd = &cobra.Command{
	Use:   "claude [-- claude-args...]",
	Short: "Run Claude Code on a box, with this project on it",
	Long: `Runs Claude Code on a box instead of on this machine, attached to your
terminal. Each run creates a box of its own (claude-<project>-<random>)
and puts the project there, at the same absolute path: a git repository
with an origin is cloned from it -- the default branch, then a new
branch boxctl/<project>-<random> for Claude to work on; nothing local,
uncommitted or unpushed, goes -- and anything else is copied. Then it
installs Claude Code and your Claude configuration, and starts Claude in
it. When Claude exits, you
detach (Ctrl-b d) or the connection drops, the box's branches and
uncommitted work are fetched here as box/* (as boxctl claude fetch does)
and the box is deleted.

--keep uses the project's own box (claude-<project>) instead, and keeps
it: Claude runs in tmux, so it keeps going when you detach or the
connection drops, and boxctl claude --keep comes back to it. The box's
copy of the project is the one Claude works on: later runs don't copy it
again, and nothing is synced back -- Claude commits and pushes from the
box. --box, --detach and --task keep their box too.

  boxctl claude                           # a new box, deleted afterwards
  boxctl claude --keep                    # create or reattach to claude-<project>
  boxctl claude --prompt "fix the flaky auth test"
  boxctl claude --detach --prompt "..."   # start it, don't attach (kept)
  boxctl claude -- --model opus           # arguments for claude itself
  boxctl claude --repo owner/name         # a fresh box, the repo cloned, on a new branch (-R for short)
  boxctl claude --goal "bump Go and fix what breaks"   # unattended, ends in a PR
  boxctl claude --repo owner/name --issue 42           # unattended: resolve issue #42, ends in a PR

The first time, log Claude in on the box with /login: it prints a URL to
open here and a code to paste back. That login stays on the box.

GitHub: while you're attached, the box borrows your gh token for git
push and gh pr ... -- it asks this machine per use, over the ssh
connection, and keeps nothing (--github forward, the default; each use is
logged to ~/.boxctl/claude/github.log). Detached, it has none. For work
that must reach GitHub while you're away, --github store keeps a token
you paste on the box -- make it a fine-grained one for this repository.
--github off gives it nothing. Your git user.name and user.email are set
on the box either way.

--repo owner/name needs no local copy (repeat it for several repositories,
cloned side by side under /root/workspace, a pull request in each): it creates a box, clones the
repository's default branch and starts Claude on a new branch,
boxctl/<slug>-<id>. With --goal, Claude instead runs unattended
(claude -p) until the goal is done and opens a pull request; the command
prints the box, the branch and how to follow it, and returns. Without
--repo, --goal uses this directory's git repository (its GitHub origin).
--issue N is a goal that only names a GitHub issue of the repository: Claude
reads it (and its comments) with gh, resolves it, and opens a pull request
that closes it; --goal then adds instructions to it. The repository is
--repo's, or this directory's origin.
That run outlives this terminal, so it can't use --github forward, which
only works while you're attached: it needs --github store.
boxctl claude logs <box> shows how it's going.`,
	RunE: claudeAgent.runs(runClaude),
}

func init() {
	claudeAgent.addFlags(claudeCmd)
	claudeCmd.Flags().StringVar(&claudeHandoff, "handoff", "", "continue this local Claude Code session (its ID) on the box")
	claudeCmd.Flags().StringSliceVarP(&claudeRepos, "repo", "R", nil, "GitHub repository (owner/name) to clone on a new box, on a new boxctl/... branch; repeat or comma-separate for several, cloned side by side (with --goal: default is this directory's origin)")
	claudeCmd.Flags().StringVar(&claudeGoal, "goal", "", "a goal for Claude to carry out unattended, ending in a pull request (needs --github store)")
	claudeCmd.Flags().IntVar(&claudeIssue, "issue", 0, "number of a GitHub issue for Claude to resolve unattended, ending in a pull request that closes it (needs --github store; one repository)")
	claudeCmd.Flags().StringVar(&claudeAuth, "claude-auth", claudeAuthAuto, "how Claude on the box logs in: auto (the token boxctl claude login saved, else /login), token, login or api-key")
	_ = claudeCmd.RegisterFlagCompletionFunc("claude-auth", cobra.FixedCompletions([]string{claudeAuthAuto, claudeAuthToken, claudeAuthLogin, claudeAuthAPIKey}, cobra.ShellCompDirectiveNoFileComp))
	rootCmd.AddCommand(claudeCmd)
}

func runClaude(cmd *cobra.Command, args []string) error {
	if len(claudeRepos) > 0 || claudeGoal != "" || claudeIssue != 0 {
		return runRepo(cmd, args)
	}
	ctx := cmd.Context()
	if err := checkSessionFlags(); err != nil {
		return err
	}
	// Everything that can fail here fails before any box is created or
	// touched: a handoff's transcript, the Claude credentials.
	transcript := ""
	if claudeHandoff != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if transcript, err = findTranscript(filepath.Join(home, ".claude", "projects"), claudeHandoff); err != nil {
			return err
		}
	}
	env, apiKey, err := claudeEnv(claudeAuth)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Independent of each other and of the project being on the box, so
	// they run while it's cloned or copied, sharing its time instead of
	// adding to it (the ssh connection is shared, see boxSSH).
	var (
		configErr, setupErr, stateErr error
		running                       bool
	)
	t, sess, err := openSession(ctx, cmd.Flags().Changed("idle-ttl"), func(box *boxSSH, sess taskSession) {
		inParallel(
			func() { configErr = copyClaudeConfig(ctx, box) },
			func() {
				if setupErr = box.run(ctx, agent.setupScript(), nil); setupErr == nil {
					sayDone("Installed %s", agent.title)
				}
			},
			func() {
				stateErr = box.run(ctx, claudeStateScript([]string{sess.dir}, localClaudeTheme(), apiKey), nil)
			},
			func() { running = sessionRunning(ctx, box, sess) },
		)
	})
	if err != nil {
		return err
	}
	defer t.end()
	name, box := t.name, t.box

	if configErr != nil {
		sayWarn("couldn't copy your Claude configuration: %v", configErr)
	}
	if setupErr != nil {
		return fmt.Errorf("installing Claude Code on %s: %w", name, setupErr)
	}
	if stateErr != nil {
		sayWarn("couldn't skip Claude's first-run setup on %s: %v", name, stateErr)
	}

	if claudeHandoff != "" {
		if running {
			return fmt.Errorf("a Claude session is already running there -- attach with %s and quit it, then hand off again", sess.attachHint(name))
		}
		if err := copyTranscript(ctx, box, sess.dir, transcript, claudeHandoff); err != nil {
			return err
		}
		args = append([]string{"--resume", claudeHandoff}, args...)
	}
	if !running {
		if env == "" && claudeAuth == claudeAuthAuto {
			sayNote("Log Claude in on the box with /login -- or run boxctl claude login once, and every box's Claude is logged in")
		}
		if err := stageClaudeEnv(ctx, box, sess, env); err != nil {
			return fmt.Errorf("passing Claude its credentials: %w", err)
		}
	}
	return startSession(ctx, t, sess, running, args)
}

// checkSessionFlags checks the flags every agent's command shares, before
// anything is done.
func checkSessionFlags() error {
	switch claudeGitHub {
	case githubForward, githubStore, githubOff:
	default:
		return fmt.Errorf("--github must be %s, %s or %s", githubForward, githubStore, githubOff)
	}
	return resolveTask()
}

// openSession is the start of an agent's run, whichever agent: the
// project's box with the project on it -- copied the first time -- the
// task's worktree, your git identity and, for --github store, a token.
//
// Unless ephemeralRun says otherwise, the box is a new one of the run's
// own, which t.end() deletes -- as openSession does itself if it fails.
//
// alongside, unless nil, is the agent's own setup on the box, which
// doesn't need the project there: it runs while the project is put there.
func openSession(ctx context.Context, setTTL bool, alongside func(*boxSSH, taskSession)) (*claudeTarget, taskSession, error) {
	ephemeral := ephemeralRun()
	if ephemeral {
		dir, err := projectDir()
		if err != nil {
			return nil, taskSession{}, err
		}
		n, err := ephemeralBoxName(ctx, dir)
		if err != nil {
			return nil, taskSession{}, err
		}
		claudeBox, claudeBranch = n.box(), n.branch()
	}
	t, err := openClaudeBox(ctx, true, setTTL)
	if err != nil {
		if ephemeral {
			discardBox(claudeBox)
		}
		return nil, taskSession{}, err
	}
	t.ephemeral = ephemeral
	if ephemeral {
		sayNote("Ephemeral: %s is deleted when %s exits or you detach, after its work is fetched here as box/* (--keep keeps it)", t.name, agent.title)
	}
	// Before the project, which a clone may need it for -- and before
	// anything runs alongside, as it may ask for the token.
	if claudeGitHub == githubStore {
		if err := storeGitHubToken(ctx, t.box, githubRepo(t.dir)); err != nil {
			t.end()
			return nil, taskSession{}, err
		}
	}
	sess := newTaskSession(t.dir, claudeTask)
	// Each step reports itself above the spinner as it finishes (sayDone).
	sp := startSpinner("Setting up " + bold(t.name))
	if alongside == nil {
		err = prepareSession(ctx, t, sess)
	} else {
		inParallel(
			func() { err = prepareSession(ctx, t, sess) },
			func() { alongside(t.box, sess) },
		)
	}
	if err != nil {
		sp.end("")
	} else {
		sp.finish(bold(t.name) + " is ready")
	}
	if err != nil {
		t.end()
		return nil, taskSession{}, err
	}
	return t, sess, nil
}

// prepareSession puts the project on t's box, unless it's there, and
// makes sess's worktree.
func prepareSession(ctx context.Context, t *claudeTarget, sess taskSession) error {
	dir, name, box := t.dir, t.name, t.box
	// Your git identity doesn't depend on the project being there yet, so
	// it's set while the clone or copy runs.
	identityErr := make(chan error, 1)
	go func() { identityErr <- copyGitIdentity(ctx, box, dir) }()
	defer func() {
		if err := <-identityErr; err != nil {
			sayWarn("couldn't set your git identity on %s: %v", name, err)
		}
	}()
	if t.needsCopy {
		// A git repository with an origin is cloned there, not copied:
		// the default branch, plus a branch of the box's own to work on:
		// a generated name's, or boxctl/<box name less the agent's>.
		if url := cloneURL(dir); url != "" {
			branch := claudeBranch
			if branch == "" {
				branch = "boxctl/" + strings.TrimPrefix(name, agent.name+"-")
			}
			if err := cloneProject(ctx, box, dir, url, branch); err != nil {
				return err
			}
		} else if err := copyProject(ctx, box, dir); err != nil {
			return err
		}
		if err := markProject(ctx, box, dir); err != nil {
			return err
		}
		t.needsCopy = false
	} else if claudeTask == "" {
		sayNote("%s is already on %s; working with the box's copy (it isn't copied again)", dir, name)
		if claudeHandoff != "" {
			sayWarn("Your local changes since then aren't on the box: commit them and run boxctl claude push, or hand off to a fresh box with --box.")
		}
	}
	if sess.task != "" {
		if err := box.run(ctx, worktreeScript(dir, sess.task), nil); err != nil {
			return fmt.Errorf("making the %s worktree on %s: %w", sess.task, name, err)
		}
	}
	return nil
}

// inParallel runs fns at once and returns when all have.
func inParallel(fns ...func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

// sessionRunning reports whether sess's agent is running in tmux on the
// box. =name: exact. A bare -t name falls back to a prefix match, so
// "claude" would find "claude-auth" and take the main session for running.
func sessionRunning(ctx context.Context, box *boxSSH, sess taskSession) bool {
	return box.run(ctx, "tmux has-session -t ="+sess.tmux+" 2>/dev/null", nil) == nil
}

// startSession starts the agent in sess with --prompt and args, unless
// it's running already, and attaches to it unless --detach says not to.
func startSession(ctx context.Context, t *claudeTarget, sess taskSession, running bool, args []string) error {
	if running && (claudePrompt != "" || len(args) > 0) {
		fmt.Fprintf(os.Stderr, "%s is already running there; attaching to it (--prompt and %s arguments only apply to a new session).\n", agent.title, agent.bin)
	}
	line := sess.commandLine(claudePrompt, args)
	if claudeDetach {
		if !running {
			if err := t.box.run(ctx, sess.tmuxCommand(line, true), nil); err != nil {
				return fmt.Errorf("starting %s on %s: %w", agent.title, t.name, err)
			}
		}
		sayDone("%s is running on %s -- attach with: %s", agent.title, bold(t.name), sess.attachHint(t.name))
		return nil
	}
	return attachClaude(ctx, t.box, sess, sess.tmuxCommand(line, false))
}

// resolveTask checks --task and --own-box, and turns --own-box into the
// task's own box: <agent>-<project>-<task>, where it's the main session.
func resolveTask() error {
	if claudeTask != "" && !taskNamePattern.MatchString(claudeTask) {
		return fmt.Errorf("--task %q: use lowercase letters, digits and dashes, at most 30", claudeTask)
	}
	if !claudeOwnBox {
		return nil
	}
	if claudeTask == "" {
		return fmt.Errorf("--own-box needs --task")
	}
	if claudeBox == "" {
		dir, err := projectDir()
		if err != nil {
			return err
		}
		claudeBox = claudeBoxName(dir) + "-" + claudeTask
	}
	claudeTask = ""
	return nil
}

// attachClaude attaches to Claude on the box, lending it GitHub
// credentials for as long as the attach lasts unless --github says not to.
func attachClaude(ctx context.Context, box *boxSSH, sess taskSession, command string) error {
	if claudeGitHub != githubForward {
		return box.attach(ctx, command, nil)
	}
	logPath := githubLogPath()
	srv, err := startTokenServer(box.name, logPath)
	if err != nil {
		sayWarn("not forwarding GitHub credentials: %v", err)
		return box.attach(ctx, command, nil)
	}
	// A socket a dropped connection left behind would stop sshd binding
	// the new one on an image without StreamLocalBindUnlink.
	_ = box.run(ctx, "rm -f "+shellQuote(sess.ghSocket), nil)
	err = box.attach(ctx, command, []string{"-R", sess.ghSocket + ":" + srv.socket})
	// Without the socket the box's helpers say "attach to forward", rather
	// than failing to reach a stale one.
	_ = box.run(context.Background(), "rm -f "+shellQuote(sess.ghSocket), nil)
	if given, refused := srv.close(); given+refused > 0 {
		used := fmt.Sprintf("%s used your GitHub token %d time(s)", box.name, given)
		if refused > 0 {
			used += fmt.Sprintf(", %d refused", refused)
		}
		sayNote("%s (%s)", used, logPath)
	}
	return err
}

// githubLogPath is where each use of the forwarded token is logged.
func githubLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".boxctl", "claude", "github.log")
}

// runForwarded runs command on the box with your gh token lent to it for
// as long as it runs (--github forward). The forward is added to the
// shared connection for that time (ssh -O forward) rather than given a
// connection of its own, which would cost a new tunnel and handshake.
func (b *boxSSH) runForwarded(ctx context.Context, command, boxSocket string) error {
	srv, err := startTokenServer(b.name, githubLogPath())
	if err != nil {
		sayWarn("not forwarding GitHub credentials: %v", err)
		return b.run(ctx, command, nil)
	}
	defer srv.close()
	fwd := boxSocket + ":" + srv.socket
	_ = b.run(ctx, "rm -f "+shellQuote(boxSocket), nil)
	if err := b.control("forward", "-R", fwd); err != nil {
		sayWarn("not forwarding GitHub credentials: %v", err)
		return b.run(ctx, command, nil)
	}
	defer func() { _ = b.control("cancel", "-R", fwd) }()
	// Without the socket the box's helpers say "attach to forward", rather
	// than failing to reach a stale one.
	return b.run(ctx, "trap "+shellQuote("rm -f "+shellQuote(boxSocket))+" EXIT\n"+command, nil)
}

// control asks the shared connection's master to do op (ssh -O op).
func (b *boxSSH) control(op string, extra ...string) error {
	argv := append(append([]string{"-O", op}, extra...), b.args...)
	out, err := exec.Command("ssh", argv...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh -O %s: %w: %s", op, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// claudeTarget is the project here and its box, reachable over ssh.
type claudeTarget struct {
	dir, name string
	box       *boxSSH
	// needsCopy: the project isn't on the box yet.
	needsCopy bool
	// ephemeral: the box is this run's, deleted by end.
	ephemeral bool
}

// openClaudeBox resolves the project and its box (--box, or
// <agent>-<dir>), makes sure it's running and reachable with boxctl's
// key, and checks it holds this project and no other. create says
// whether a missing box is created (boxctl claude, kilo) or an error (fetch,
// push); setTTL applies --idle-ttl to a box that already exists.
func openClaudeBox(ctx context.Context, create, setTTL bool) (*claudeTarget, error) {
	dir, err := projectDir()
	if err != nil {
		return nil, err
	}
	name := claudeBox
	if name == "" {
		name = claudeBoxName(dir)
	}
	box, err := connectClaudeBox(ctx, name, create, setTTL)
	if err != nil {
		return nil, err
	}
	needsCopy, err := checkProject(ctx, box, dir)
	if err != nil {
		box.close()
		return nil, err
	}
	return &claudeTarget{dir: dir, name: name, box: box, needsCopy: needsCopy}, nil
}

// connectClaudeBox makes sure the box name is running (created first when
// create is set), authorized for boxctl's key and reachable over ssh,
// with its idle TTL set when it's new or setTTL says to.
func connectClaudeBox(ctx context.Context, name string, create, setTTL bool) (*boxSSH, error) {
	c := newClient()
	created, err := ensureClaudeBox(ctx, c, name, create)
	if err != nil {
		return nil, err
	}
	key, err := ensureClaudeKey()
	if err != nil {
		return nil, err
	}
	// Two independent API calls: no reason to wait for one before the other.
	var ttlErr, authErr error
	inParallel(
		func() {
			if created || setTTL {
				_, ttlErr = c.SetIdleTTL(ctx, name, claudeIdle)
			}
		},
		func() { authErr = authorizeKey(ctx, c, name, key) },
	)
	if ttlErr != nil {
		sayWarn("couldn't set %s's idle TTL: %v", name, ttlErr)
	}
	if authErr != nil {
		return nil, authErr
	}
	box, err := newBoxSSH(name, key)
	if err != nil {
		return nil, err
	}
	if err := box.run(ctx, "true", nil); err != nil {
		box.close()
		return nil, fmt.Errorf("can't ssh into %s: %w", name, err)
	}
	return box, nil
}

// checkProject reports whether dir still has to be copied to the box,
// refusing a box that holds a different project. A box from before the
// marker existed is taken to hold whichever project it has at dir.
func checkProject(ctx context.Context, box *boxSSH, dir string) (needsCopy bool, err error) {
	out, err := box.output(ctx, projectCheckScript(dir))
	switch status := strings.TrimSpace(string(out)); {
	case err != nil:
		return false, fmt.Errorf("checking %s's project: %w", box.name, err)
	case status == "copy":
		return true, nil
	case status == "ok":
		return false, nil
	case strings.HasPrefix(status, "other "):
		other := strings.TrimPrefix(status, "other ")
		return false, fmt.Errorf("%s holds %s, not %s -- run boxctl %s from there, or pick another box with --box", box.name, other, dir, agent.name)
	default:
		return false, fmt.Errorf("checking %s's project: unexpected %q", box.name, status)
	}
}

func projectCheckScript(dir string) string { return projectCheckScriptAt(projectMarker, dir) }

func projectCheckScriptAt(marker, dir string) string {
	d := shellQuote(dir)
	return `m=` + shellQuote(marker) + `
if [ -s "$m" ] && [ "$(cat "$m")" != ` + d + ` ]; then printf 'other %s' "$(cat "$m")"; exit 0; fi
if [ -e ` + d + ` ]; then mkdir -p "$(dirname "$m")" && printf '%s' ` + d + ` >"$m"; echo ok; exit 0; fi
echo copy`
}

// markProject records dir as the box's project, and lets boxctl claude
// push update the branch checked out there (receive.denyCurrentBranch
// updateInstead: the working tree follows the push, and git refuses it if
// Claude has uncommitted changes, rather than overwriting them).
func markProject(ctx context.Context, box *boxSSH, dir string) error {
	d := shellQuote(dir)
	return box.run(ctx, `mkdir -p "$(dirname `+projectMarker+`)" && printf '%s' `+d+` >`+projectMarker+`
[ ! -d `+d+`/.git ] || git -C `+d+` config receive.denyCurrentBranch updateInstead`, nil)
}

// projectDir is the git repository's top level, or the directory itself
// outside one, for --project or else the current directory -- the
// project goes to the same absolute path on the box.
func projectDir() (string, error) {
	start := claudeProject
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		start = wd
	}
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(start); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s isn't a directory", start)
	}
	if out, err := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel").Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			return dir, nil
		}
	}
	return start, nil
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// claudeBoxName is the project's box for the running agent:
// <agent>-<directory name, slugged, at most 20 chars>, so claude-<dir>
// for Claude.
func claudeBoxName(dir string) string {
	slug := slugUnsafe.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	if len(slug) > 20 {
		slug = slug[:20]
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "project"
	}
	return agent.name + "-" + slug
}

// ensureClaudeBox resumes name if it exists, or creates it when create
// is set, reporting whether it did.
func ensureClaudeBox(ctx context.Context, c *client.Client, name string, create bool) (created bool, err error) {
	vms, err := c.List(ctx)
	if err != nil {
		return false, err
	}
	for _, vm := range vms {
		if vm.Name == name {
			return false, ensureRunning(ctx, c, name, claudeReadyTimeout)
		}
	}
	if !create {
		return false, fmt.Errorf("no box %s -- start one with boxctl %s", name, agent.name)
	}
	detail := dim("(" + claudeImage + ", " + claudeSize + ")")
	sp := startSpinner(fmt.Sprintf("Creating %s %s", bold(name), detail))
	if _, err := c.Create(ctx, name, "", claudeImage, claudeSize); err != nil {
		sp.fail(fmt.Sprintf("%s: %v", name, err))
		return false, fmt.Errorf("creating %s: %w", name, err)
	}
	if _, err := c.WaitReady(ctx, name, claudeReadyTimeout); err != nil {
		sp.fail(fmt.Sprintf("%s: %v", name, err))
		return true, err
	}
	sp.finish(fmt.Sprintf("Created %s %s", bold(name), detail))
	return true, nil
}

// ensureClaudeKey returns the private key boxctl uses for boxes it runs
// Claude on -- the Claude Code plugin's key, so either tool can reach a
// box the other set up -- generating it the first time.
func ensureClaudeKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	key := filepath.Join(home, ".boxctl", "claude", "id_ed25519")
	if _, err := os.Stat(key); err == nil {
		return key, nil
	}
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		return "", err
	}
	out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "boxctl-claude", "-f", key).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh-keygen: %w: %s", err, out)
	}
	return key, nil
}

// authorizeKey adds key's public half to the box's root authorized_keys
// over boxctl's exec endpoint -- the one way in that needs no key.
func authorizeKey(ctx context.Context, c *client.Client, name, key string) error {
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return err
	}
	line := shellQuote(strings.TrimSpace(string(pub)))
	script := "mkdir -p ~/.ssh && chmod 700 ~/.ssh && { grep -qxF " + line + " ~/.ssh/authorized_keys 2>/dev/null || echo " + line + " >>~/.ssh/authorized_keys; }"
	res, err := c.Exec(ctx, name, script, 30*time.Second)
	if err != nil {
		return fmt.Errorf("authorizing boxctl's key on %s: %w", name, err)
	}
	if res.Error != "" || res.ExitCode != 0 {
		return fmt.Errorf("authorizing boxctl's key on %s: %s%s", name, res.Error, res.Stderr)
	}
	return nil
}

// boxSSH runs ssh to one box: through `boxctl ssh-proxy`, as root, with
// boxctl's key, and over one shared connection (ControlMaster) so each
// step after the first costs a round trip, not a new tunnel and
// handshake.
type boxSSH struct {
	name string
	args []string // everything before the remote command
}

func newBoxSSH(name, key string) (*boxSSH, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, fmt.Errorf("boxctl %s needs ssh on this machine", agent.name)
	}
	control := filepath.Join(filepath.Dir(key), "cm-%C")
	return &boxSSH{name: name, args: []string{
		// None of the user's ssh config applies -- least of all macOS's
		// SendEnv LC_*, which the box has no locales for.
		"-F", "/dev/null",
		"-i", key, "-o", "IdentitiesOnly=yes",
		// The tunnel is already authenticated end to end (your boxctl
		// token, over TLS to boxctl.io), and every box has fresh host
		// keys, so there's nothing for known_hosts to add.
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		"-o", "ProxyCommand=" + shellQuote(self) + " ssh-proxy --running " + shellQuote(name),
		"-o", "ServerAliveInterval=30",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + control, "-o", "ControlPersist=60s",
		"root@" + name,
	}}, nil
}

// run runs a command on the box, with stdin from in (nil for none) and
// its output passed through to stderr.
func (b *boxSSH) run(ctx context.Context, command string, in io.Reader) error {
	cmd := exec.CommandContext(ctx, "ssh", append(append([]string{"-o", "BatchMode=yes"}, b.args...), command)...)
	cmd.Stdin = in
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// output runs a command on the box and returns its stdout; stderr passes
// through.
func (b *boxSSH) output(ctx context.Context, command string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", append(append([]string{"-o", "BatchMode=yes"}, b.args...), command)...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// stream runs a command on the box with its output on ours, until it ends
// or ctx is cancelled.
func (b *boxSSH) stream(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "ssh", append(append([]string{"-o", "BatchMode=yes"}, b.args...), command)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// gitSSHCommand is ssh as git should run it to reach the box: everything
// in b.args but the destination, which git adds from the remote's URL.
func (b *boxSSH) gitSSHCommand() string {
	words := []string{"ssh", "-o", "BatchMode=yes"}
	for _, a := range b.args[:len(b.args)-1] {
		words = append(words, shellQuote(a))
	}
	return strings.Join(words, " ")
}

// attach runs command on the box with a terminal, returning when it ends,
// with extra ssh arguments (a socket forward). It gets a connection of its
// own rather than sharing the control master, so a forward lives exactly
// as long as the attach: ssh takes the first value of an option it's
// given, so ControlPath=none here beats the shared one in b.args.
func (b *boxSSH) attach(ctx context.Context, command string, extra []string) error {
	cmd := exec.CommandContext(ctx, "ssh", b.attachArgs(command, extra)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The box has terminfo for the common terminals, not for every one
	// (xterm-ghostty, xterm-kitty), and tmux won't start without it.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Detaching or quitting Claude ends ssh normally; anything else
		// was already printed by ssh or the box.
		return nil
	}
	return err
}

func (b *boxSSH) attachArgs(command string, extra []string) []string {
	argv := append([]string{"-t", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=no"}, extra...)
	return append(append(argv, b.args...), command)
}

// close ends the shared ssh connection.
func (b *boxSSH) close() {
	_ = exec.Command("ssh", append(append([]string{"-O", "exit"}, b.args[:len(b.args)-1]...), b.args[len(b.args)-1])...).Run()
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

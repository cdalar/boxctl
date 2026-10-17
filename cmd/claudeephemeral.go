package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// An ephemeral run -- boxctl claude (or kilo) without --keep, --box,
// --detach or --task -- gets a box of its own, created for this run and
// deleted when it ends: the session quits, you detach, or setup fails.
// Not when the connection drops: that's reconnected, and if you give up
// on it the box is kept, its session still running (see startSession). Before the box goes, its branches and
// uncommitted work are fetched here as box/* (the same as boxctl claude
// fetch), so nothing Claude did is lost with it.

// ephemeralRun reports whether this run's box is deleted when it ends.
// Call it after checkSessionFlags, which turns --own-box into --box.
func ephemeralRun() bool {
	return !claudeKeep && claudeBox == "" && !claudeDetach && claudeTask == ""
}

// ephemeralBoxName is a fresh box name for this project:
// <project>-<slug>-<id>, the slug from --prompt if there is one (see
// boxName).
func ephemeralBoxName(ctx context.Context, dir string) (boxName, error) {
	return newBoxName(ctx, filepath.Base(dir), claudePrompt)
}

// saveBoxWork fetches the box's branches and uncommitted work as box/*,
// if the project is a git repository that made it onto the box. It
// reports whether the box can go: not when there was work to fetch and
// fetching it failed.
func saveBoxWork(ctx context.Context, t *claudeTarget) bool {
	if t.needsCopy || exec.CommandContext(ctx, "git", "-C", t.dir, "rev-parse", "--git-dir").Run() != nil {
		return true
	}
	g := &claudeGitTarget{t}
	sp := startSpinner("Fetching " + bold(t.name) + "'s work")
	err := g.ensureRemote(ctx)
	if err == nil {
		err = fetchBox(ctx, g)
	}
	if err != nil {
		sp.fail(fmt.Sprintf("Couldn't fetch %s's work (%v), so it's kept: get it with boxctl %s fetch --box %s, then boxctl rm %s.", t.name, err, agent.name, t.name, t.name))
		return false
	}
	sp.end("")
	return true
}

// repoWorkPushed reports whether a --repo run's box can go: Claude has
// quit, and nothing in its clones is only on the box -- no uncommitted
// or stashed changes, no commit that isn't on a remote-tracking branch.
// Whether a pull request was opened doesn't matter; one can be opened
// from a pushed branch any time. A run that changed nothing can go too.
// There's no local checkout to fetch into, so otherwise the box is kept.
func repoWorkPushed(ctx context.Context, t *claudeTarget) bool {
	if sessionRunning(ctx, t.box, t.sess) {
		sayNote("%s is still running on %s, so it's kept -- attach with: %s", agent.title, bold(t.name), t.sess.attachHint(t.name))
		return false
	}
	out, err := t.box.output(ctx, unpushedScript(t.repoDirs))
	if err != nil {
		sayWarn("couldn't check %s for work that isn't on GitHub (%v), so it's kept: boxctl rm %s when you're done with it", t.name, err, t.name)
		return false
	}
	if left := strings.TrimSpace(string(out)); left != "" {
		sayWarn("%s has work that isn't on GitHub, so it's kept:\n  %s\nPush it from boxctl %s --box %s, or boxctl rm %s to drop it.", t.name, strings.ReplaceAll(left, "\n", "\n  "), agent.name, t.name, t.name)
		return false
	}
	return true
}

// unpushedScript prints a line for each thing in dirs that is only on
// the box. It reads remote-tracking refs as the last push or fetch left
// them, without asking GitHub: once you've detached there may be no
// credentials to ask with, and a push updates them anyway. A dir that
// was never cloned (setup failed first) is skipped.
func unpushedScript(dirs []string) string {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = shellQuote(d)
	}
	return `for d in ` + strings.Join(quoted, " ") + `; do
  [ -d "$d/.git" ] || continue
  ( cd "$d" || exit
    [ -z "$(git status --porcelain)" ] || echo "$d: uncommitted changes"
    [ -z "$(git stash list)" ] || echo "$d: stashed changes"
    git for-each-ref --format='%(refname:short)' refs/heads | while read -r b; do
      n=$(git rev-list --count "refs/heads/$b" --not --remotes=origin)
      [ "$n" = 0 ] || echo "$d: $n commit(s) on $b not pushed"
    done )
done`
}

// end closes the ssh to t's box and, for an ephemeral run, saves its
// work and deletes it. It runs on a context of its own: the run's may
// have been cancelled by Ctrl-C.
func (t *claudeTarget) end() {
	discard := false
	if t.ephemeral {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if t.repoDirs != nil {
			discard = repoWorkPushed(ctx, t)
		} else {
			discard = saveBoxWork(ctx, t)
		}
		cancel()
	}
	t.box.close()
	if discard {
		discardBox(t.name)
	}
}

// discardBox deletes this run's box.
func discardBox(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sp := startSpinner("Deleting " + bold(name))
	if err := newClient().Destroy(ctx, name); err != nil {
		sp.fail(fmt.Sprintf("Couldn't delete %s: %v -- delete it with boxctl rm %s", name, err, name))
		return
	}
	sp.finish("Deleted " + bold(name))
}

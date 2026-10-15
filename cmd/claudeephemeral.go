package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// An ephemeral run -- boxctl claude (or kilo) without --keep, --box,
// --detach or --task -- gets a box of its own, created for this run and
// deleted when it ends: the session quits, you detach, the connection
// drops, or setup fails. Before the box goes, its branches and
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
	err := g.ensureRemote(ctx)
	if err == nil {
		err = fetchBox(ctx, g)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Couldn't fetch %s's work (%v), so it's kept: get it with boxctl %s fetch --box %s, then boxctl rm %s.\n", t.name, err, agent.name, t.name, t.name)
		return false
	}
	return true
}

// end closes the ssh to t's box and, for an ephemeral run, saves its
// work and deletes it. It runs on a context of its own: the run's may
// have been cancelled by Ctrl-C.
func (t *claudeTarget) end() {
	discard := false
	if t.ephemeral {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		discard = saveBoxWork(ctx, t)
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
	fmt.Fprintf(os.Stderr, "Deleting %s...\n", name)
	if err := newClient().Destroy(ctx, name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't delete %s: %v -- delete it with boxctl rm %s\n", name, err, name)
		return
	}
	fmt.Fprintf(os.Stderr, "Deleted %s.\n", name)
}

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

// How a --goal run went, as the box's runner records it
// (<goalDir>/status.json, written by goalRun.runnerScript), and what
// boxctl does with that: `claude ls` lists it, and a finished run's box is
// paused once nothing uses it (deleted, if it opened a pull request),
// rather than running on until its idle TTL.
//
// The status lives on the box, so a paused box's can't be read without
// resuming it. Each one read is kept in ~/.boxctl/claude/goals.json, which
// is what `claude ls` shows for a paused box.

// goalStatus is status.json.
type goalStatus struct {
	Status  string   `json:"status,omitempty"` // running, done or failed
	Branch  string   `json:"branch,omitempty"`
	PRURL   string   `json:"pr_url,omitempty"`  // the first
	PRURLs  []string `json:"pr_urls,omitempty"` // one per repository, with --repo given more than once
	Error   string   `json:"error,omitempty"`
	Updated string   `json:"updated,omitempty"`
	// Projects are the box's project directories (runInfoScript). Not
	// in status.json: ls adds them, for the cache.
	Projects []string `json:"projects,omitempty"`
}

func (s goalStatus) finished() bool { return s.Status == "done" || s.Status == "failed" }

// goalPauseAfter is how long a finished goal's box has to have gone
// unused before `claude ls` pauses it, so a box someone is looking into
// isn't paused under them.
const goalPauseAfter = 10 * time.Minute

// goalStatusScript prints the latest goal's status.json, or nothing for a
// box without a goal.
const goalStatusScript = `cat ` + boxGoalsDir + `/latest/status.json 2>/dev/null || true`

// runInfoScript prints the box's project directories on a line of their
// own, tab-separated -- the project marker, or for a multi-repository
// workspace each clone in it -- then goalStatusScript's output.
const runInfoScript = `p=$(cat ` + projectMarker + ` 2>/dev/null)
if [ "$p" = ` + boxWorkspace + ` ]; then for d in "$p"/*/; do [ -d "$d" ] && printf '%s\t' "${d%/}"; done; else printf '%s' "$p"; fi
echo; ` + goalStatusScript

// parseRunInfo splits runInfoScript's output into the project directories
// and the goal's status (nil without a goal).
func parseRunInfo(out []byte) ([]string, *goalStatus, error) {
	line, rest, _ := strings.Cut(string(out), "\n")
	var projects []string
	for _, p := range strings.Split(line, "\t") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	st, err := parseGoalStatus([]byte(rest))
	return projects, st, err
}

// parseGoalStatus is nil for a box without a goal (no status.json).
func parseGoalStatus(out []byte) (*goalStatus, error) {
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var s goalStatus
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, fmt.Errorf("reading the goal's status: %w", err)
	}
	return &s, nil
}

func readGoalStatus(ctx context.Context, box *boxSSH) (*goalStatus, error) {
	out, err := box.output(ctx, goalStatusScript)
	if err != nil {
		return nil, err
	}
	return parseGoalStatus(out)
}

// goalSettled is what settleFinishedGoal did with a finished goal's box.
type goalSettled int

const (
	goalKept goalSettled = iota
	goalPaused
	goalDeleted
)

// hasPR reports whether the run opened a pull request: its work is on
// GitHub then, and the box has nothing left that isn't. The box wrote the
// URL, so only a GitHub one counts.
func (s goalStatus) hasPR() bool {
	return s.Status == "done" && strings.HasPrefix(s.PRURL, "https://github.com/")
}

// settleFinishedGoal deals with vm, whose goal has finished, when it's
// running and unused: either the caller knows it is, or the server hasn't
// seen it used for goalPauseAfter. A run that opened a pull request has
// its work on GitHub, so its box is deleted; any other (failed, or done
// without a PR) is paused, its log and branch still on it.
func settleFinishedGoal(ctx context.Context, c *client.Client, vm *client.VM, st *goalStatus, unused bool) (goalSettled, error) {
	if !st.finished() {
		return goalKept, nil
	}
	if !unused && (vm.LastActivityAt == nil || time.Since(*vm.LastActivityAt) < goalPauseAfter) {
		return goalKept, nil
	}
	if st.hasPR() {
		if err := c.Destroy(ctx, vm.Name); err != nil {
			return goalKept, fmt.Errorf("deleting %s: %w", vm.Name, err)
		}
		fmt.Fprintf(os.Stderr, "Deleted %s: its goal is done and the pull request is open (%s).\n", vm.Name, st.PRURL)
		return goalDeleted, nil
	}
	if _, err := c.Pause(ctx, vm.Name); err != nil {
		return goalKept, fmt.Errorf("pausing %s: %w", vm.Name, err)
	}
	fmt.Fprintf(os.Stderr, "Paused %s: its goal is %s. boxctl resume %s to look at it again.\n", vm.Name, st.Status, vm.Name)
	return goalPaused, nil
}

// forgetGoal drops a deleted box's remembered status.
func forgetGoal(name string) {
	p := goalCachePath()
	if p == "" {
		return
	}
	m := loadGoalCache()
	if _, ok := m[name]; !ok {
		return
	}
	delete(m, name)
	if b, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

func findVM(ctx context.Context, c *client.Client, name string) (*client.VM, error) {
	vms, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range vms {
		if vms[i].Name == name {
			return &vms[i], nil
		}
	}
	return nil, fmt.Errorf("no box %s", name)
}

// runBoxID is the -<id> a generated box name ends with.
var runBoxID = regexp.MustCompile(`-[0-9a-f]{4}$`)

// goalBoxPrefix starts a --goal (or --issue) run's generated box name:
// goal-<repository>-<slug>-<id>. box() cuts the repository short before
// it, never this.
const goalBoxPrefix = "goal-"

// isGoalBox reports whether name is a goal run's generated box name. Unlike
// isRunBox it needs no project: claude ls lists every goal run, from any
// directory, since a goal needs no checkout to start from.
func isGoalBox(name string) bool {
	return strings.HasPrefix(name, goalBoxPrefix) && runBoxID.MatchString(name)
}

// isRunBox reports whether name is a generated box name (boxName.box)
// with one of prefixes: a --goal, --repo or ephemeral run's box. box()
// may have cut the prefix short, but never below minPrefix: what's left
// of boxNameMax after the longest slug and id.
func isRunBox(name string, prefixes []string) bool {
	if !runBoxID.MatchString(name) {
		return false
	}
	minPrefix := boxNameMax - slugMax - len("-0000") - 1
	for _, p := range prefixes {
		p = nameSlug(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(name, p+"-") {
			return true
		}
		for i := minPrefix; i < len(p); i++ {
			if cut := strings.Trim(p[:i], "-"); cut != "" && strings.HasPrefix(name, cut+"-") {
				return true
			}
		}
	}
	return false
}

// goalCachePath is where the goal statuses last read are kept.
func goalCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".boxctl", "claude", "goals.json")
}

func loadGoalCache() map[string]goalStatus {
	m := map[string]goalStatus{}
	if p := goalCachePath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(b, &m)
		}
	}
	return m
}

// rememberGoals adds statuses to the cache and, given the names of every
// box there is, forgets the boxes that are gone. Best effort: it's only
// what ls shows for a paused box.
func rememberGoals(statuses map[string]goalStatus, boxes map[string]bool) {
	p := goalCachePath()
	if p == "" {
		return
	}
	m := loadGoalCache()
	for name, s := range statuses {
		m[name] = s
	}
	if boxes != nil {
		for name := range m {
			if !boxes[name] {
				delete(m, name)
			}
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

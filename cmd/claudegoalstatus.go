package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
)

// How a --goal run went, as the box's runner records it
// (<goalDir>/status.json, written by goalRun.runnerScript), which the
// box also reports to the server over vsock (boxctl-status): `claude ls`
// and `boxctl ls` read that, a box's run_status, and never the file.
// Neither pauses or deletes a box: the server pauses a finished goal's
// (boxctl.pause-when-done).

// goalStatus is status.json.
type goalStatus struct {
	Status  string   `json:"status,omitempty"` // running, done or failed
	Branch  string   `json:"branch,omitempty"`
	PRURL   string   `json:"pr_url,omitempty"`  // the first
	PRURLs  []string `json:"pr_urls,omitempty"` // one per repository, with --repo given more than once
	Error   string   `json:"error,omitempty"`
	Updated string   `json:"updated,omitempty"`
}

func (s goalStatus) finished() bool { return s.Status == "done" || s.Status == "failed" }

// parseGoalStatus is nil for a box without a goal (no status.json, or
// no run_status).
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

// goalBoxPrefix starts a --goal (or --issue) run's generated box name:
// goal-<repository>-<slug>-<id>. box() cuts the repository short before
// it, never this.
const goalBoxPrefix = "goal-"

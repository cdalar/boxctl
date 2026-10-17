package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

func TestParseGoalStatus(t *testing.T) {
	if s, err := parseGoalStatus([]byte("\n")); s != nil || err != nil {
		t.Errorf("no goal: %v %v", s, err)
	}
	s, err := parseGoalStatus([]byte(`{"status":"done","branch":"boxctl/x-ab12","pr_url":"https://github.com/o/n/pull/7","error":"","updated":"2026-10-16T09:00:00Z"}` + "\n"))
	if err != nil || s.Status != "done" || !s.finished() || s.PRURL != "https://github.com/o/n/pull/7" {
		t.Errorf("done: %+v %v", s, err)
	}
	if (goalStatus{Status: "running"}).finished() {
		t.Error("running is finished")
	}
}

// TestLogsFollowStops runs logs -f's script against a goal that finishes
// while it's followed: it has to end, with the final status.
func TestLogsFollowStops(t *testing.T) {
	d := t.TempDir()
	latest := filepath.Join(d, "latest")
	if err := os.MkdirAll(latest, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(latest, "status.json"), `{"status":"running"}`)
	write(t, filepath.Join(latest, "claude.log"), "line one\n")
	script := strings.ReplaceAll(logsScript(true), boxGoalsDir, d)
	c := exec.Command("sh", "-c", script)
	go func() {
		time.Sleep(time.Second)
		write(t, filepath.Join(latest, "status.json"), `{"status":"done"}`)
	}()
	done := make(chan []byte)
	go func() {
		out, _ := c.CombinedOutput()
		done <- out
	}()
	select {
	case out := <-done:
		if !strings.Contains(string(out), "line one") || !strings.HasSuffix(strings.TrimSpace(string(out)), `{"status":"done"}`) {
			t.Errorf("output: %s", out)
		}
	case <-time.After(20 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("logs -f didn't stop when the goal finished")
	}
}

// TestGoalBoxName: cut short, a goal box's name still starts goal-.
func TestGoalBoxName(t *testing.T) {
	long := boxName{prefix: nameSlug(goalBoxPrefix + "a-really-long-repository-name"), slug: "fix-flaky-login-test", id: "a1b2"}
	if b := long.box(); len(b) > boxNameMax || !strings.HasPrefix(b, goalBoxPrefix) {
		t.Errorf("%q: cut short, it should still start %s", b, goalBoxPrefix)
	}
}

func TestRunText(t *testing.T) {
	for _, c := range []struct {
		vm   client.VM
		want string
	}{
		{client.VM{}, "-"},
		{client.VM{Labels: map[string]string{"boxctl.run": "box", "boxctl.agent": "claude"}}, "claude box"},
		{client.VM{Labels: map[string]string{"boxctl.run": "session"}}, "session"},
		{client.VM{Labels: map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude"}, RunStatus: []byte(`{"status":"done"}`)}, "claude goal: done"},
		{client.VM{Labels: map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude"}}, "claude goal: ?"}, // never reported
		{client.VM{Labels: map[string]string{"boxctl.run": "goal"}, RunStatus: []byte(`not json`)}, "goal: ?"},
	} {
		if got := runText(c.vm); got != c.want {
			t.Errorf("runText(%v) = %q, want %q", c.vm.Labels, got, c.want)
		}
	}
}

func TestRunRow(t *testing.T) {
	goal := client.VM{
		Labels:    map[string]string{"boxctl.run": "goal", "boxctl.branch": "boxctl/x-ab12"},
		RunStatus: []byte(`{"status":"done","branch":"boxctl/x-cd34","pr_url":"https://github.com/o/n/pull/7","pr_urls":["https://github.com/o/n/pull/7","https://github.com/o/m/pull/8"],"updated":"` + time.Now().UTC().Format(time.RFC3339) + `"}`),
	}
	if g, u, b, pr := runRow(goal); g != "done" || u != "just now" || b != "boxctl/x-cd34" || pr != "https://github.com/o/n/pull/7 (+1 more)" {
		t.Errorf("goal: %q %q %q %q", g, u, b, pr)
	}
	// Not reported: the branch from its label, the goal unknown.
	goal.RunStatus = nil
	if g, _, b, pr := runRow(goal); g != "?" || b != "boxctl/x-ab12" || pr != "-" {
		t.Errorf("unreported goal: %q %q %q", g, b, pr)
	}
	// A --repo run has no goal to report.
	if g, _, _, _ := runRow(client.VM{Labels: map[string]string{"boxctl.run": "repo"}}); g != "-" {
		t.Errorf("repo run: %q", g)
	}
	// Only a GitHub URL is shown as one.
	evil := client.VM{Labels: map[string]string{"boxctl.run": "goal"}, RunStatus: []byte(`{"status":"done","pr_url":"https://evil.example/x"}`)}
	if _, _, _, pr := runRow(evil); pr != "-" {
		t.Errorf("non-GitHub PR shown: %q", pr)
	}
}

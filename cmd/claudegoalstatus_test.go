package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestIsRunBox(t *testing.T) {
	long := boxName{prefix: nameSlug("a-really-long-repository-name"), slug: "fix-flaky-login-test", id: "a1b2"}
	for _, c := range []struct {
		name string
		want bool
	}{
		{"boxctl-fix-login-a1b2", true},
		{"boxctl-zesty-comet-f959", true},
		{long.box(), true},
		{"boxctl-fix-login", false},         // no id
		{"claude-boxctl", false},            // the project's own box
		{"other-fix-login-a1b2", false},     // another project's
		{"box-fix-login-a1b2", false},       // a prefix of the prefix isn't it
		{"boxctl-web-fix-login-a1b2", true}, // can't tell boxctl-web's apart by name
	} {
		if got := isRunBox(c.name, []string{"boxctl", "a-really-long-repository-name"}); got != c.want {
			t.Errorf("isRunBox(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

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

// TestRunInfoScript runs ls's probe against a single clone and a
// workspace of two.
func TestRunInfoScript(t *testing.T) {
	probe := func(marker string, clones ...string) []string {
		root := t.TempDir()
		ws := filepath.Join(root, "workspace")
		for _, c := range clones {
			if err := os.MkdirAll(filepath.Join(ws, c), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if marker == boxWorkspace {
			marker = ws
		}
		write(t, filepath.Join(root, "project"), marker)
		r := strings.NewReplacer(projectMarker, filepath.Join(root, "project"), boxWorkspace, ws, boxGoalsDir, filepath.Join(root, "goals"))
		out, err := exec.Command("sh", "-c", r.Replace(runInfoScript)).Output()
		if err != nil {
			t.Fatal(err)
		}
		dirs, st, err := parseRunInfo(out)
		if err != nil || st != nil {
			t.Fatalf("status %v, %v", st, err)
		}
		for i, d := range dirs {
			dirs[i] = strings.TrimPrefix(d, ws+"/")
		}
		return dirs
	}
	if got := probe("/root/boxctl"); len(got) != 1 || got[0] != "/root/boxctl" {
		t.Errorf("one clone: %q", got)
	}
	if got := probe(boxWorkspace, "boxctl", "boxctl-web"); len(got) != 2 || !slices.Contains(got, "boxctl") || !slices.Contains(got, "boxctl-web") {
		t.Errorf("workspace: %q", got)
	}
}

func TestIsGoalBox(t *testing.T) {
	long := boxName{prefix: nameSlug(goalBoxPrefix + "a-really-long-repository-name"), slug: "fix-flaky-login-test", id: "a1b2"}
	if b := long.box(); len(b) > boxNameMax || !isGoalBox(b) {
		t.Errorf("%q: cut short, it should still be a goal box", b)
	}
	for name, want := range map[string]bool{
		"goal-onctl-runners-issue-29-0ca8": true,
		"goal-boxctl-fix-login":            false, // no id
		"boxctl-fix-login-a1b2":            false, // a run, not a goal
		"onctl-runners-issue-29-0ca8":      false, // a goal from before goal- names: isRunBox's
	} {
		if got := isGoalBox(name); got != want {
			t.Errorf("isGoalBox(%q) = %v, want %v", name, got, want)
		}
	}
}

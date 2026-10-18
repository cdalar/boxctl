package cmd

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseRepo(t *testing.T) {
	for in, want := range map[string]string{"cdalar/boxctl": "cdalar/boxctl", "a/b.git": "a/b", " o/n-1_x.y ": "o/n-1_x.y"} {
		o, n, err := parseRepo(in)
		if err != nil || o+"/"+n != want {
			t.Errorf("parseRepo(%q) = %s/%s, %v", in, o, n, err)
		}
	}
	for _, in := range []string{"", "boxctl", "a/b/c", "a/", "/b", "a b/c", "https://github.com/a/b"} {
		if _, _, err := parseRepo(in); err == nil {
			t.Errorf("parseRepo(%q) succeeded", in)
		}
	}
}

func TestCheckRepoFlags(t *testing.T) {
	if err := checkRepoFlags("", githubOff, nil); err != nil {
		t.Errorf("--repo alone with off: %v", err)
	}
	if err := checkRepoFlags("do it", githubStore, nil); err != nil {
		t.Errorf("goal with store: %v", err)
	}
	err := checkRepoFlags("do it", githubOff, nil)
	if err == nil || !strings.Contains(err.Error(), "--github off") || !strings.Contains(err.Error(), "BOXCTL_GITHUB_TOKEN") {
		t.Errorf("goal with off: %v", err)
	}
	if err := checkRepoFlags("", githubStore, []string{"--task", "--handoff"}); err == nil || !strings.Contains(err.Error(), "--task, --handoff") {
		t.Errorf("conflicts: %v", err)
	}
}

func TestGoalPrompt(t *testing.T) {
	p := goalPrompt("fix the bug", []string{"o/n"}, []string{"/root/n"}, "boxctl/fix-the-bug-ab12cd")
	for _, want := range []string{"fix the bug\n", "o/n", "git push -u origin boxctl/fix-the-bug-ab12cd", "gh pr create", "word for word"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

func TestCloneScript(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	run(t, root, "git", "init", "-q", "-b", "main", origin)
	write(t, filepath.Join(origin, "a.txt"), "a")
	run(t, origin, "git", "-c", "user.name=t", "-c", "user.email=t@t", "add", ".")
	run(t, origin, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	// Cloned from a local repository rather than GitHub.
	dir := filepath.Join(root, "box", "n")
	script := cloneScript(origin, dir, "boxctl/x-ab12cd")
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != "boxctl/x-ab12cd" {
		t.Errorf("HEAD = %q, %v", head, err)
	}
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err == nil || !strings.Contains(string(out), "already exists") {
		t.Errorf("second clone: %v %s", err, out)
	}
}

// goalOutcome runs a goal's runner script with a claude and a gh that
// behave as given, and returns status.json and the staged env file's fate.
func goalOutcome(t *testing.T, claude, gh string) (status, log string, envLeft bool) {
	t.Helper()
	return goalOutcomeIn(t, t.TempDir(), "", claude, gh)
}

// goalOutcomeIn is goalOutcome in root, with binDir (if any) first on
// PATH.
func goalOutcomeIn(t *testing.T, root, binDir, claude, gh string) (status, log string, envLeft bool) {
	t.Helper()
	home, project := filepath.Join(root, "home"), filepath.Join(root, "project")
	goalDir := filepath.Join(root, "goal")
	for _, d := range []string{filepath.Join(home, ".local", "bin"), project, goalDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envFile := filepath.Join(root, "env")
	write(t, envFile, "export CLAUDE_CODE_OAUTH_TOKEN=secret\n")
	write(t, filepath.Join(goalDir, "prompt.txt"), "the goal\nline two")
	write(t, filepath.Join(home, ".local", "bin", "claude"), "#!/bin/sh\n"+claude)
	write(t, filepath.Join(root, "gh"), "#!/bin/sh\n"+gh)
	for _, f := range []string{filepath.Join(home, ".local", "bin", "claude"), filepath.Join(root, "gh")} {
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g := goalRun{dir: project, branch: "boxctl/x-ab12cd", goalDir: goalDir, envFile: envFile, args: []string{"--model", "opus"}, gh: filepath.Join(root, "gh")}
	c := exec.Command("sh", "-c", g.runnerScript())
	path := "/usr/bin:/bin"
	if binDir != "" {
		path = binDir + ":" + path
	}
	c.Env = []string{"HOME=" + home, "PATH=" + path}
	if out, err := c.CombinedOutput(); err != nil {
		t.Logf("runner: %v %s", err, out)
	}
	s, _ := os.ReadFile(filepath.Join(goalDir, "status.json"))
	l, _ := os.ReadFile(filepath.Join(goalDir, "claude.log"))
	_, statErr := os.Stat(envFile)
	return strings.TrimSpace(string(s)), string(l), statErr == nil
}

func TestGoalRunnerDone(t *testing.T) {
	status, log, envLeft := goalOutcome(t,
		`echo "token=$CLAUDE_CODE_OAUTH_TOKEN sandbox=$IS_SANDBOX cwd=$(basename "$PWD") args=$*"`,
		`echo https://github.com/o/n/pull/7`)
	if !strings.Contains(status, `"status":"done"`) || !strings.Contains(status, `"pr_url":"https://github.com/o/n/pull/7"`) || !strings.Contains(status, `"branch":"boxctl/x-ab12cd"`) {
		t.Errorf("status = %s", status)
	}
	for _, want := range []string{"token=secret", "sandbox=1", "cwd=project", "-p the goal", "--dangerously-skip-permissions", "--model opus"} {
		if !strings.Contains(log, want) {
			t.Errorf("claude's log lacks %q: %s", want, log)
		}
	}
	if envLeft {
		t.Error("the env file wasn't deleted")
	}
}

func TestGoalRunnerFailed(t *testing.T) {
	status, _, _ := goalOutcome(t, `echo trouble; exit 3`, `echo https://github.com/o/n/pull/7`)
	if !strings.Contains(status, `"status":"failed"`) || !strings.Contains(status, "exited with status 3") {
		t.Errorf("claude failed: %s", status)
	}
	status, _, _ = goalOutcome(t, `echo done`, `exit 0`)
	if !strings.Contains(status, `"status":"failed"`) || !strings.Contains(status, `"pr_url":""`) || !strings.Contains(status, "without opening a pull request") {
		t.Errorf("no PR: %s", status)
	}
}

func TestLogsScript(t *testing.T) {
	if s := logsScript(false); strings.Contains(s, "-f") || !strings.Contains(s, "status.json") {
		t.Error(s)
	}
	if s := logsScript(true); !strings.Contains(s, "tail -n 200 -f "+boxGoalsDir+"/latest/claude.log") {
		t.Error(s)
	}
}

func TestParseRepos(t *testing.T) {
	got, err := parseRepos([]string{"a/x", "b/y.git", "a/x"})
	if err != nil || strings.Join(got, ",") != "a/x,b/y" {
		t.Errorf("parseRepos = %v, %v", got, err)
	}
	if _, err := parseRepos([]string{"a/x", "b/x"}); err == nil || !strings.Contains(err.Error(), "both be cloned") {
		t.Errorf("same name, different owners: %v", err)
	}
	if _, err := parseRepos([]string{"a/x", "nope"}); err == nil {
		t.Error("a bad repo was accepted")
	}
}

func TestRepoDirs(t *testing.T) {
	dirs, work := repoDirs([]string{"o/n"})
	if len(dirs) != 1 || dirs[0] != "/root/n" || work != "/root/n" {
		t.Errorf("one repo: %v %s", dirs, work)
	}
	dirs, work = repoDirs([]string{"o/a", "p/b"})
	if strings.Join(dirs, ",") != boxWorkspace+"/a,"+boxWorkspace+"/b" || work != boxWorkspace {
		t.Errorf("two repos: %v %s", dirs, work)
	}
}

func TestGoalPromptSeveralRepos(t *testing.T) {
	p := goalPrompt("bump it", []string{"o/a", "p/b"}, []string{"/root/workspace/a", "/root/workspace/b"}, "boxctl/bump-ab12cd")
	for _, want := range []string{"bump it\n", "- o/a, in /root/workspace/a", "- p/b, in /root/workspace/b", "boxctl/bump-ab12cd", "for each repository you changed", "word for word"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

// With several repositories the runner asks gh in each clone, and reports
// every pull request it finds.
func TestGoalRunnerSeveralRepos(t *testing.T) {
	root := t.TempDir()
	home, goalDir := filepath.Join(root, "home"), filepath.Join(root, "goal")
	a, b, c := filepath.Join(root, "ws", "a"), filepath.Join(root, "ws", "b"), filepath.Join(root, "ws", "c")
	for _, d := range []string{filepath.Join(home, ".local", "bin"), a, b, c, goalDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(goalDir, "prompt.txt"), "the goal")
	write(t, filepath.Join(home, ".local", "bin", "claude"), "#!/bin/sh\necho ok")
	// gh knows a pull request only in a and c, by the directory it runs in.
	write(t, filepath.Join(root, "gh"), "#!/bin/sh\ncase \"$PWD\" in */a) echo https://github.com/o/a/pull/1;; */c) echo https://github.com/o/c/pull/3;; esac")
	for _, f := range []string{filepath.Join(home, ".local", "bin", "claude"), filepath.Join(root, "gh")} {
		if err := os.Chmod(f, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	g := goalRun{dir: filepath.Join(root, "ws"), repoDirs: []string{a, b, c}, branch: "boxctl/x-ab12cd", goalDir: goalDir, envFile: filepath.Join(root, "none"), gh: filepath.Join(root, "gh")}
	cmd := exec.Command("sh", "-c", g.runnerScript())
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runner: %v %s", err, out)
	}
	s, _ := os.ReadFile(filepath.Join(goalDir, "status.json"))
	for _, want := range []string{`"status":"done"`, `"pr_url":"https://github.com/o/a/pull/1"`, `"pr_urls":["https://github.com/o/a/pull/1","https://github.com/o/c/pull/3"]`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("status lacks %s: %s", want, s)
		}
	}
}

func TestIssueGoal(t *testing.T) {
	g := issueGoal("o/n", 42, "")
	for _, want := range []string{"issue #42 of o/n", "gh issue view 42 --repo o/n --comments", `"Closes #42"`} {
		if !strings.Contains(g, want) {
			t.Errorf("goal lacks %q:\n%s", want, g)
		}
	}
	if strings.HasSuffix(g, "\n") {
		t.Errorf("goal without extra instructions ends in a newline: %q", g)
	}
	if g := issueGoal("o/n", 42, " keep it small \n"); !strings.HasSuffix(g, "\n\nkeep it small") {
		t.Errorf("extra instructions not appended: %q", g)
	}
	// The slug a box and branch get is from "issue N".
	if got := textSlug("issue 42"); got != "issue-42" {
		t.Errorf("textSlug(issue 42) = %q", got)
	}
}

// The runner reports each status to the control plane through the
// image's boxctl-status, handing it status.json -- not a copy, so the
// sender can re-read it (the newest status wins a race).
func TestGoalRunnerReports(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(root, "reports")
	// setsid too: boxes have it, macOS doesn't.
	write(t, filepath.Join(bin, "setsid"), "#!/bin/sh\nexec \"$@\"\n")
	// A slow sender: the runner must still wait for it.
	write(t, filepath.Join(bin, "boxctl-status"), "#!/bin/sh\nsleep 0.3\necho \"$1 $(cat \"$1\")\" >>"+rec+"\n")
	for _, f := range []string{"setsid", "boxctl-status"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	status, _, _ := goalOutcomeIn(t, root, bin, `echo ok`, `echo https://github.com/o/n/pull/7`)
	if !strings.Contains(status, `"status":"done"`) {
		t.Fatalf("status = %s", status)
	}
	// The runner waits for its reports, so both are in by the time it
	// returns: a run that ends must not lose its last one.
	b, _ := os.ReadFile(rec)
	got := string(b)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("reports: %q, want running then done", got)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, filepath.Join(root, "goal", "status.json")+" {") {
			t.Errorf("report %q: want status.json's path, then its content", l)
		}
	}
	if !strings.Contains(lines[len(lines)-1], `"status":"done"`) {
		t.Errorf("last report %q isn't done", lines[len(lines)-1])
	}
}

func TestSessionLabels(t *testing.T) {
	agent = claudeAgent
	dir := t.TempDir() // not a git repository: no boxctl.repo
	got := sessionLabels("session", dir, "boxctl/fix-a1b2")
	want := map[string]string{"boxctl.run": "session", "boxctl.agent": "claude", "boxctl.project": dir, "boxctl.branch": "boxctl/fix-a1b2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sessionLabels = %v, want %v", got, want)
	}
	if got := sessionLabels("box", "/"+strings.Repeat("d", 300), ""); got["boxctl.project"] != "" || got["boxctl.branch"] != "" {
		t.Errorf("sessionLabels of a long dir = %v, want no project or branch", got)
	}
}

func TestRunLabels(t *testing.T) {
	agent = claudeAgent
	got := runLabels(true, []string{"cdalar/onctl-runners"}, "boxctl/issue-29-0ca8", 29)
	want := map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude", "boxctl.branch": "boxctl/issue-29-0ca8", "boxctl.repo": "cdalar/onctl-runners", "boxctl.issue": "29", "boxctl.pause-when-done": "1h"}
	if !maps.Equal(got, want) {
		t.Errorf("goal labels = %v, want %v", got, want)
	}
	got = runLabels(false, []string{"o/a", "o/b"}, "boxctl/x-1234", 0)
	if got["boxctl.run"] != "repo" || got["boxctl.repo"] != "o/a,o/b" || got["boxctl.issue"] != "" || got["boxctl.pause-when-done"] != "" {
		t.Errorf("repo labels = %v", got)
	}
	if got = runLabels(false, []string{strings.Repeat("o/r", 100)}, "b", 0); got["boxctl.repo"] != "" {
		t.Error("an over-long repo label was set")
	}
}

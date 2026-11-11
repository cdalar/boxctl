package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"
)

// setModelFlags sets --model/--effort for a test, and resets them after.
func setModelFlags(t *testing.T, model, effort string) {
	t.Helper()
	claudeModelFlag, claudeEffortFlag = model, effort
	t.Cleanup(func() { claudeModelFlag, claudeEffortFlag = "", "" })
}

func TestResolveModelPrecedence(t *testing.T) {
	h := &harness{label: "h", model: &harnessModel{Model: "harness-model", Effort: "low"}}
	conf := &config.Config{ClaudeModel: "config-model", ClaudeEffort: "max"}
	tests := []struct {
		name                  string
		flagModel, flagEffort string
		h                     *harness
		envModel, envEffort   string
		conf                  *config.Config
		args                  []string
		model, effort         string
		claudeArgs            []string
	}{
		{name: "nothing chosen: the copied settings apply", conf: &config.Config{}},
		{name: "config", conf: conf, model: "config-model", effort: "max",
			claudeArgs: []string{"--model", "config-model", "--effort", "max"}},
		{name: "environment beats config", envModel: "env-model", envEffort: "medium", conf: conf,
			model: "env-model", effort: "medium", claudeArgs: []string{"--model", "env-model", "--effort", "medium"}},
		{name: "environment model alone, config effort", envModel: "env-model", conf: conf,
			model: "env-model", effort: "max", claudeArgs: []string{"--model", "env-model", "--effort", "max"}},
		{name: "harness beats environment", h: h, envModel: "env-model", envEffort: "medium", conf: conf,
			model: "harness-model", effort: "low", claudeArgs: []string{"--model", "harness-model", "--effort", "low"}},
		{name: "flags beat everything", flagModel: "claude-sonnet-5-5", flagEffort: "medium", h: h, envModel: "env-model", envEffort: "high", conf: conf,
			model: "claude-sonnet-5-5", effort: "medium", claudeArgs: []string{"--model", "claude-sonnet-5-5", "--effort", "medium"}},
		{name: "passthrough beats the flag", flagModel: "claude-sonnet-5-5", flagEffort: "medium", args: []string{"--model", "haiku"},
			model: "haiku", effort: "medium", claudeArgs: []string{"--effort", "medium"}},
		{name: "passthrough --effort=", envModel: "env-model", envEffort: "high", args: []string{"--effort=low"},
			model: "env-model", effort: "low", claudeArgs: []string{"--model", "env-model"}},
		{name: "effort is case-insensitive", flagEffort: "XHigh", effort: "xhigh", claudeArgs: []string{"--effort", "xhigh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setModelFlags(t, tt.flagModel, tt.flagEffort)
			t.Setenv(envClaudeModel, tt.envModel)
			t.Setenv(envClaudeEffort, tt.envEffort)
			c, err := resolveModel(tt.args, modelSources(tt.h, tt.conf))
			if err != nil {
				t.Fatal(err)
			}
			if c.model != tt.model || c.effort != tt.effort {
				t.Errorf("model, effort = %q, %q; want %q, %q", c.model, c.effort, tt.model, tt.effort)
			}
			if got := c.args(); !slices.Equal(got, tt.claudeArgs) {
				t.Errorf("args = %q, want %q", got, tt.claudeArgs)
			}
		})
	}
}

func TestResolveModelRefusesUnknownEffort(t *testing.T) {
	for _, tt := range []struct{ flag, env, from string }{
		{flag: "hgih", from: "--effort"},
		{env: "turbo", from: envClaudeEffort},
	} {
		setModelFlags(t, "", tt.flag)
		t.Setenv(envClaudeEffort, tt.env)
		_, err := resolveModel(nil, modelSources(nil, nil))
		if err == nil || !strings.Contains(err.Error(), tt.from) || !strings.Contains(err.Error(), "low, medium, high, xhigh, max") {
			t.Errorf("effort %q%q: err = %v, want one naming %s and the levels", tt.flag, tt.env, err, tt.from)
		}
	}
	// What's passed through is claude's to check.
	setModelFlags(t, "", "")
	t.Setenv(envClaudeEffort, "")
	if _, err := resolveModel([]string{"--effort", "whatever"}, modelSources(nil, nil)); err != nil {
		t.Errorf("passthrough effort: %v", err)
	}
}

// TestGoalRunnerCarriesModel: what runRepo hands the runner for --goal
// --model claude-sonnet-5-5 --effort medium ends up on claude -p's line.
func TestGoalRunnerCarriesModel(t *testing.T) {
	setModelFlags(t, "claude-sonnet-5-5", "medium")
	t.Setenv(envClaudeModel, "fable")
	c, err := resolveModel(nil, modelSources(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	g := goalRun{dir: "/root/x", branch: "boxctl/b", goalDir: "/root/.boxctl/goals/1", envFile: "/run/boxctl/s.env", args: c.args()}
	want := `claude -p "$(cat "$d/prompt.txt")" --dangerously-skip-permissions --output-format stream-json --verbose '--model' 'claude-sonnet-5-5' '--effort' 'medium' >>`
	if s := g.runnerScript(); !strings.Contains(s, want) {
		t.Errorf("runner script lacks\n%s\n---\n%s", want, s)
	}

	// No flags: the environment's model, and no --effort at all.
	setModelFlags(t, "", "")
	c, _ = resolveModel(nil, modelSources(nil, &config.Config{}))
	g.args = c.args()
	if s := g.runnerScript(); !strings.Contains(s, `--verbose '--model' 'fable' >>`) {
		t.Errorf("runner script without flags doesn't use %s:\n%s", envClaudeModel, s)
	}
}

func TestSessionCommandCarriesModel(t *testing.T) {
	c := modelChoice{model: "sonnet", effort: "high"}
	s := newTaskSession("/root/p", "")
	line := s.commandLine("hi", append(c.args(), "--verbose"))
	if !strings.Contains(line, `claude '--model' 'sonnet' '--effort' 'high' '--verbose' 'hi'`) {
		t.Errorf("command line = %s", line)
	}
}

func TestHarnessModelFile(t *testing.T) {
	dir := t.TempDir()
	writeHarnessFile(t, dir, harnessModelFile, `{"model":"sonnet","effort":"medium"}`)
	h, err := loadHarness(dir) // a harness may be nothing but its model
	if err != nil {
		t.Fatal(err)
	}
	if h.model == nil || h.model.Model != "sonnet" || h.model.Effort != "medium" {
		t.Errorf("model = %+v", h.model)
	}
	if slices.Contains(h.config, harnessModelFile) || slices.Contains(h.managed, harnessModelFile) {
		t.Errorf("%s would be copied to the box", harnessModelFile)
	}

	bad := t.TempDir()
	writeHarnessFile(t, bad, harnessModelFile, `{"modle":"sonnet"}`)
	if _, err := loadHarness(bad); err == nil {
		t.Error("a misspelled key was accepted")
	}
}

func TestModelLabels(t *testing.T) {
	runModel = &modelChoice{model: "claude-sonnet-5-5", effort: "medium"}
	t.Cleanup(func() { runModel = nil })
	got := withModelLabels(map[string]string{"boxctl.run": "goal"})
	want := map[string]string{"boxctl.run": "goal", labelModel: "claude-sonnet-5-5", labelEffort: "medium"}
	if !mapsEqual(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
	runModel = &modelChoice{}
	if got := withModelLabels(map[string]string{"boxctl.run": "goal"}); len(got) != 1 {
		t.Errorf("nothing chosen still labels: %v", got)
	}

	for _, tt := range []struct {
		labels map[string]string
		want   string
	}{
		{nil, "-"},
		{map[string]string{labelModel: "sonnet"}, "sonnet"},
		{map[string]string{labelModel: "sonnet", labelEffort: "high"}, "sonnet (high)"},
		{map[string]string{labelEffort: "high"}, "- (high)"},
	} {
		if got := modelText(client.VM{Labels: tt.labels}); got != tt.want {
			t.Errorf("modelText(%v) = %q, want %q", tt.labels, got, tt.want)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// captureStdout returns what fn writes to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(&buf, r) }()
	defer func() {
		os.Stdout = old
	}()
	fn()
	_ = w.Close()
	wg.Wait()
	return buf.String()
}

func TestClaudeListAndStatusShowModel(t *testing.T) {
	vms := []client.VM{
		{Name: "goal-x-fix-1", State: "running", Labels: map[string]string{"boxctl.run": "goal", labelModel: "claude-sonnet-5-5", labelEffort: "medium"}},
		{Name: "goal-x-docs-2", State: "running", Labels: map[string]string{"boxctl.run": "goal"}},
	}
	out := captureStdout(t, func() {
		if err := printBoxes(t.Context(), vms, "table", true, "", true); err != nil {
			t.Error(err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "MODEL") {
		t.Fatalf("ls:\n%s", out)
	}
	if !strings.Contains(lines[1]+lines[2], "claude-sonnet-5-5 (medium)") {
		t.Errorf("ls doesn't show the model:\n%s", out)
	}
	if f := strings.Fields(lines[2]); !strings.HasPrefix(lines[2], "goal-x-docs-2") || f[len(f)-2] != "-" {
		t.Errorf("ls's MODEL for nothing chosen isn't a dash:\n%s", out)
	}
	// boxctl ls keeps its columns.
	out = captureStdout(t, func() { _ = printBoxes(t.Context(), vms, "table", true, "", false) })
	if strings.Contains(out, "MODEL") {
		t.Errorf("boxctl ls got a MODEL column:\n%s", out)
	}

	out = captureStdout(t, func() { printRunStatus(vms[0]) })
	if !strings.Contains(out, "Model:   claude-sonnet-5-5 (medium)\n") {
		t.Errorf("status:\n%s", out)
	}
	out = captureStdout(t, func() { printRunStatus(vms[1]) })
	if !strings.Contains(out, "Model:   -\n") {
		t.Errorf("status with nothing chosen:\n%s", out)
	}
}

// TestInvalidEffortFailsFast runs the real command against a fake server:
// a bad --effort (or BOXCTL_CLAUDE_EFFORT) is refused before any request
// that makes a box.
func TestInvalidEffortFailsFast(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode([]client.VM{})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(tokenEnv, "tok")
	t.Cleanup(func() {
		claudeGoal, claudeRepos, claudeEffortFlag, claudeModelFlag, apiURLFlag = "", nil, "", "", ""
		_ = claudeCmd.Flags().Set("effort", "")
		rootCmd.SetArgs(nil)
	})
	for _, tt := range []struct {
		args []string
		env  string
	}{
		{args: []string{"--effort", "extreme"}},
		{env: "extreme"},
	} {
		t.Setenv(envClaudeEffort, tt.env)
		rootCmd.SetArgs(append([]string{"--api-url", srv.URL, "claude", "--repo", "o/r", "--goal", "x"}, tt.args...))
		err := rootCmd.Execute()
		if err == nil || !strings.Contains(err.Error(), `effort "extreme"`) {
			t.Errorf("%v %s: err = %v", tt.args, tt.env, err)
		}
		_ = claudeCmd.Flags().Set("effort", "")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range calls {
		if c != "GET /api/vms" {
			t.Errorf("request before the effort was checked: %s", c)
		}
	}
}

package cmd

import (
	"slices"
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestRunParts(t *testing.T) {
	status := `{"status":"done","branch":"boxctl/fix-1a2b","pr_url":"https://github.com/cdalar/boxctl/pull/96","pr_urls":["https://github.com/cdalar/boxctl/pull/96","https://github.com/cdalar/boxctl-web/pull/7"]}`
	vm := client.VM{Name: "goal-x", Labels: map[string]string{"boxctl.run": "goal", "boxctl.agent": "claude", "boxctl.repo": "cdalar/boxctl,cdalar/boxctl-web"}, RunStatus: []byte(status)}
	parts, c, ok := runParts(vm)
	if !ok {
		t.Fatal("no cell for a box with a PR")
	}
	wantParts := []string{"claude goal: done", "cdalar/boxctl +1", "boxctl/fix-1a2b", "https://github.com/cdalar/boxctl/pull/96 +1"}
	if !slices.Equal(parts, wantParts) {
		t.Errorf("parts = %q, want %q", parts, wantParts)
	}
	line := runLine(parts, false)
	wantLine := "  claude goal: done · cdalar/boxctl +1 · boxctl/fix-1a2b · https://github.com/cdalar/boxctl/pull/96 +1"
	if line != wantLine {
		t.Errorf("line = %q, want %q", line, wantLine)
	}
	want := prCell{col: len([]rune("  claude goal: done · cdalar/boxctl +1 · boxctl/fix-1a2b · ")), url: "https://github.com/cdalar/boxctl/pull/96", rest: " +1"}
	if c != want {
		t.Errorf("got %+v, want %+v", c, want)
	}

	// A PR with nothing else to say is the whole line; a box with
	// nothing at all has no line.
	parts, c, ok = runParts(client.VM{RunStatus: []byte(`{"status":"done","pr_url":"https://github.com/o/a/pull/1"}`)})
	if !ok || c.col != len(runIndent) || !slices.Equal(parts, []string{"https://github.com/o/a/pull/1"}) {
		t.Errorf("PR alone: parts %q, cell %+v, ok %v", parts, c, ok)
	}
	if parts, _, ok := runParts(client.VM{Name: "plain"}); ok || len(parts) != 0 || runLine(parts, false) != "" {
		t.Errorf("unlabeled box: parts %q, ok %v", parts, ok)
	}
	for name, vm := range map[string]client.VM{
		"no PR yet":  {Name: "goal-y", Labels: map[string]string{"boxctl.run": "goal"}, RunStatus: []byte(`{"status":"running"}`)},
		"not GitHub": {Name: "goal-z", Labels: map[string]string{"boxctl.run": "goal"}, RunStatus: []byte(`{"status":"done","pr_url":"https://example.com/pull/1"}`)},
	} {
		parts, _, ok := runParts(vm)
		if ok || len(parts) != 1 {
			t.Errorf("%s: parts %q, ok %v", name, parts, ok)
		}
	}
}

func TestParsePRState(t *testing.T) {
	if s, err := parsePRState([]byte(`{"state":"MERGED"}`)); err != nil || s != "MERGED" {
		t.Errorf("got %q, %v", s, err)
	}
	for _, bad := range []string{``, `{}`, `not json`} {
		if _, err := parsePRState([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

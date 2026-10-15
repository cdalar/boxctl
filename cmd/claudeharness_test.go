package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func writeHarnessFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadHarness(t *testing.T) {
	dir := t.TempDir()
	writeHarnessFile(t, dir, "CLAUDE.md", "never merge")
	writeHarnessFile(t, dir, "managed-settings.json", `{"permissions":{"deny":["Bash(gh pr merge:*)"]}}`)
	writeHarnessFile(t, dir, "commands/open-pr.md", "open a PR")
	writeHarnessFile(t, dir, "skills/x/node_modules/junk.js", "skipped")
	writeHarnessFile(t, dir, "README.txt", "not part of a harness")

	h, err := loadHarness(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.managed) != 2 {
		t.Errorf("managed = %v, want CLAUDE.md and managed-settings.json", h.managed)
	}
	if len(h.config) != 1 || h.config[0] != "commands/open-pr.md" {
		t.Errorf("config = %v, want only commands/open-pr.md", h.config)
	}
}

func TestLoadHarnessRejects(t *testing.T) {
	empty := t.TempDir()
	if _, err := loadHarness(empty); err == nil {
		t.Error("an empty directory is not a harness")
	}
	bad := t.TempDir()
	writeHarnessFile(t, bad, "managed-settings.json", `{not json`)
	if _, err := loadHarness(bad); err == nil {
		t.Error("invalid managed-settings.json should be refused before a box exists")
	}
}

func TestResolveHarnessName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeHarnessFile(t, filepath.Join(home, ".boxctl", "harness", "pr-only"), "CLAUDE.md", "x")
	got, err := resolveHarness("pr-only")
	if err != nil || got != filepath.Join(home, ".boxctl", "harness", "pr-only") {
		t.Errorf("resolveHarness(pr-only) = %q, %v", got, err)
	}
	if _, err := resolveHarness("nope"); err == nil {
		t.Error("an unknown name should fail")
	}
}

func TestStarterHarnessLoads(t *testing.T) {
	h, err := loadHarness("harness-pr-only")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.managed) != 2 || len(h.config) != 1 {
		t.Errorf("managed = %v, config = %v", h.managed, h.config)
	}
}

func TestPrepareHarnessChoice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Cleanup(func() { claudeHarnessArg, claudeGoal, claudeHarness = "", "", nil })
	pick := func(arg, goal string) *harness {
		t.Helper()
		claudeHarnessArg, claudeGoal = arg, goal
		cleanup, err := prepareHarness()
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		return claudeHarness
	}

	if pick("", "") != nil {
		t.Error("a plain run with no default gets no harness")
	}
	if h := pick("", "fix it"); h == nil || h.dir != "built-in pr-only" {
		t.Errorf("--goal gets the built-in pr-only, got %+v", h)
	}
	if pick("none", "fix it") != nil {
		t.Error("--harness none turns the goal default off")
	}

	def := filepath.Join(home, ".boxctl", "harness", "default")
	writeHarnessFile(t, def, "CLAUDE.md", "mine")
	if h := pick("", ""); h == nil || h.dir != def {
		t.Errorf("a default directory applies to every run, got %+v", h)
	}
	if h := pick("", "fix it"); h == nil || h.dir != def {
		t.Errorf("your default beats the built-in for --goal, got %+v", h)
	}
	if pick("none", "") != nil {
		t.Error("--harness none turns your default off")
	}
	other := t.TempDir()
	writeHarnessFile(t, other, "CLAUDE.md", "x")
	if h := pick(other, "fix it"); h == nil || h.dir != other {
		t.Errorf("an explicit --harness wins, got %+v", h)
	}
}

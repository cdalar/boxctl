package cmd

import (
	"archive/tar"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/boxctl-io/boxctl-cli/internal/client"
)

// --harness: rules for Claude on the box that don't belong in your own
// ~/.claude, which also steers the Claude on this machine. A harness is
// a directory:
//
//	CLAUDE.md             -> /etc/claude-code/CLAUDE.md (managed memory:
//	                         loaded in every session, can't be excluded)
//	managed-settings.json -> /etc/claude-code/managed-settings.json (the
//	                         highest-precedence settings: permission
//	                         rules the user and project settings can't undo)
//	agents/ skills/ commands/ -> ~/.claude, on top of your own
//
// Everything is checked before any box is created.

const harnessManagedDir = "/etc/claude-code"

// harnessManaged are the harness files that go to harnessManagedDir.
var harnessManaged = []string{"CLAUDE.md", "managed-settings.json"}

// harnessConfigEntries are the harness entries that go to ~/.claude.
var harnessConfigEntries = []string{"agents", "skills", "commands"}

type harness struct {
	dir     string   // where it's read from
	label   string   // how it's named in output: dir, or "built-in pr-only"
	managed []string // files for harnessManagedDir, relative to dir
	config  []string // files for ~/.claude, relative to dir
	size    int64
	model   *harnessModel // its model.json (harnessModelFile), if any
	// modelFrom names the harness model came from when it isn't this
	// one: a model-only harness under the built-in (withRules).
	modelFrom string
}

// rules says whether the harness has anything for the box -- rules for
// Claude -- rather than only boxctl's model.json.
func (h *harness) rules() bool { return len(h.managed) > 0 || len(h.config) > 0 }

// errNoHarness is resolveHarness's "nothing by that name": the one
// failure the built-in's name falls back from.
var errNoHarness = errors.New("no such directory (a bare name is looked up in ~/.boxctl/harness)")

// resolveHarness is the directory --harness names: the path itself, or
// else a name under ~/.boxctl/harness.
func resolveHarness(arg string) (string, error) {
	if st, err := os.Stat(arg); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("--harness %s: not a directory", arg)
		}
		return arg, nil
	}
	if !strings.ContainsAny(arg, `/\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			p := filepath.Join(home, ".boxctl", "harness", arg)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("--harness %s: %w", arg, errNoHarness)
}

// loadHarness reads and checks the harness in dir.
func loadHarness(dir string) (*harness, error) {
	h := &harness{dir: dir, label: dir}
	for _, name := range harnessManaged {
		st, err := os.Stat(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("harness %s: %s is not a regular file", dir, name)
		}
		if name == "managed-settings.json" {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			var v map[string]json.RawMessage
			if err := json.Unmarshal(data, &v); err != nil {
				return nil, fmt.Errorf("harness %s: %s: %w", dir, name, err)
			}
		}
		h.managed = append(h.managed, name)
		h.size += st.Size()
	}
	files, size, err := configFilesIn(dir, harnessConfigEntries)
	if err != nil {
		return nil, err
	}
	h.config, h.size = files, h.size+size
	if h.model, err = loadHarnessModel(dir); err != nil {
		return nil, err
	}
	if len(h.managed) == 0 && len(h.config) == 0 && h.model == nil {
		return nil, fmt.Errorf("harness %s has none of %s, %s, %s", dir,
			strings.Join(harnessManaged, ", "), strings.Join(harnessConfigEntries, ", "), harnessModelFile)
	}
	return h, nil
}

// apply puts the harness on the box. Its ~/.claude entries overwrite
// your configuration's, so call it after copyClaudeConfig, not alongside.
func (h *harness) apply(ctx context.Context, box *boxSSH) error {
	if !h.rules() {
		return nil // only a model.json, which is boxctl's, not the box's
	}
	progress := newUploadProgress("Copying the harness "+bold(h.label), h.size)
	if len(h.managed) > 0 {
		err := streamTar(ctx, box, "mkdir -p "+harnessManagedDir+" && tar -xzf - -C "+harnessManagedDir+" && chmod 0644 "+harnessManagedDir+"/*", progress,
			func(tw *tar.Writer) error { return addFiles(tw, h.dir, h.managed) })
		if err != nil {
			return fmt.Errorf("harness: %w", err)
		}
	}
	if len(h.config) > 0 {
		err := streamTar(ctx, box, "mkdir -p ~/.claude && tar -xzf - -C ~/.claude", progress,
			func(tw *tar.Writer) error { return addFiles(tw, h.dir, h.config) })
		if err != nil {
			return fmt.Errorf("harness: %w", err)
		}
	}
	sayDone("Copied the harness %s %s", bold(h.label), dim("("+client.FormatBytes(h.size)+")"))
	return nil
}

// Which harness a run gets, first match:
//
//	--harness none            none
//	--harness <dir|name>      that one; pr-only is the built-in unless
//	                          ~/.boxctl/harness/pr-only is one of yours
//	~/.boxctl/harness/default your default, for every run
//	--goal / --issue          the built-in pr-only: it runs unattended with
//	                          --dangerously-skip-permissions, so it is the run
//	                          that most needs a guardrail you didn't have to ask for
//
// A harness with nothing but a model.json has no rules, so for --goal and
// --issue it doesn't stand in for the built-in: its model and the
// built-in's rules both apply (withRules). Whatever is picked besides an
// explicit --harness is announced, and so is the built-in under one.

const (
	harnessNone   = "none"
	harnessPROnly = "pr-only" // the built-in, by name
)

//go:embed harness-pr-only
var builtinPROnly embed.FS

var (
	claudeHarnessArg string
	claudeHarness    *harness
)

// prepareHarness picks and loads the run's harness into claudeHarness.
// The returned func removes anything it left in the temp directory.
func prepareHarness() (func(), error) {
	claudeHarness = nil
	nothing := func() {}
	switch arg := claudeHarnessArg; {
	case arg == harnessNone:
		return nothing, nil
	case arg != "":
		dir, err := resolveHarness(arg)
		if arg == harnessPROnly && errors.Is(err, errNoHarness) {
			// None of your own by that name: the built-in one.
			return builtinHarness()
		}
		if err != nil {
			return nothing, err
		}
		if claudeHarness, err = loadHarness(dir); err != nil {
			return nothing, err
		}
		return withRules(nothing, "--harness "+dir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".boxctl", "harness", "default")
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			var lerr error
			if claudeHarness, lerr = loadHarness(dir); lerr != nil {
				return nothing, lerr
			}
			if claudeHarness.rules() || !unattendedRun() {
				sayNote("Using your default harness %s (--harness %s to skip it)", dir, harnessNone)
			}
			return withRules(nothing, "your default harness "+dir)
		}
	}
	if !unattendedRun() {
		return nothing, nil
	}
	cleanup, err := builtinHarness()
	if err == nil {
		sayNote("An unattended run gets the built-in pr-only harness: pull request, never merge (--harness %s to skip it, or --harness <dir> for your own)", harnessNone)
	}
	return cleanup, err
}

// unattendedRun: --goal or --issue, which get the built-in by default.
func unattendedRun() bool { return claudeGoal != "" || claudeIssue != 0 }

// withRules keeps a model-only claudeHarness (named by what) from
// standing in for an unattended run's rules: the built-in pr-only takes
// its place, carrying its model. Anything else is left as it is, with
// cleanup the func prepareHarness returns.
func withRules(cleanup func(), what string) (func(), error) {
	own := claudeHarness
	if own == nil || own.rules() || !unattendedRun() {
		return cleanup, nil
	}
	c, err := builtinHarness()
	if err != nil {
		return cleanup, err
	}
	claudeHarness.model, claudeHarness.modelFrom = own.model, own.label
	sayNote("Using the %s of %s, which has no rules, so this unattended run also gets the built-in pr-only harness: pull request, never merge (--harness %s to skip both)", harnessModelFile, what, harnessNone)
	return c, nil
}

// builtinHarness loads the embedded pr-only harness into claudeHarness,
// extracted to a temp directory the returned func removes.
func builtinHarness() (func(), error) {
	nothing := func() {}
	tmp, err := os.MkdirTemp("", "boxctl-harness-")
	if err != nil {
		return nothing, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	dir := filepath.Join(tmp, harnessPROnly)
	if err := extractEmbedded(builtinPROnly, "harness-pr-only", dir); err != nil {
		cleanup()
		return nothing, err
	}
	if claudeHarness, err = loadHarness(dir); err != nil {
		cleanup()
		return nothing, err
	}
	// dir stays the temp directory the files are read from; only the
	// name shown changes.
	claudeHarness.label = "built-in " + harnessPROnly
	return cleanup, nil
}

// extractEmbedded writes the tree of fsys under root to dst.
func extractEmbedded(fsys embed.FS, root, dst string) error {
	return fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := fsys.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}

// applyHarness is the harness step of starting a box: a no-op without
// --harness.
func applyHarness(ctx context.Context, box *boxSSH) error {
	if claudeHarness == nil {
		return nil
	}
	return claudeHarness.apply(ctx, box)
}

package cmd

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
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
	dir     string
	managed []string // files for harnessManagedDir, relative to dir
	config  []string // files for ~/.claude, relative to dir
	size    int64
}

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
	return "", fmt.Errorf("--harness %s: no such directory (a bare name is looked up in ~/.boxctl/harness)", arg)
}

// loadHarness reads and checks the harness in dir.
func loadHarness(dir string) (*harness, error) {
	h := &harness{dir: dir}
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
	if len(h.managed) == 0 && len(h.config) == 0 {
		return nil, fmt.Errorf("harness %s has none of %s, %s", dir,
			strings.Join(harnessManaged, ", "), strings.Join(harnessConfigEntries, ", "))
	}
	return h, nil
}

// apply puts the harness on the box.
func (h *harness) apply(ctx context.Context, box *boxSSH) error {
	fmt.Fprintf(os.Stderr, "Copying the harness %s (%s) to %s...\n", h.dir, client.FormatBytes(h.size), box.name)
	if len(h.managed) > 0 {
		err := streamTar(ctx, box, "mkdir -p "+harnessManagedDir+" && tar -xzf - -C "+harnessManagedDir+" && chmod 0644 "+harnessManagedDir+"/*", nil,
			func(tw *tar.Writer) error { return addFiles(tw, h.dir, h.managed) })
		if err != nil {
			return fmt.Errorf("harness: %w", err)
		}
	}
	if len(h.config) > 0 {
		err := streamTar(ctx, box, "mkdir -p ~/.claude && tar -xzf - -C ~/.claude", nil,
			func(tw *tar.Writer) error { return addFiles(tw, h.dir, h.config) })
		if err != nil {
			return fmt.Errorf("harness: %w", err)
		}
	}
	return nil
}

// harnessFlag is --harness; claudeHarness what it loaded, nil without it.
var (
	claudeHarnessArg string
	claudeHarness    *harness
)

// prepareHarness loads --harness, if given, into claudeHarness.
func prepareHarness() error {
	claudeHarness = nil
	if claudeHarnessArg == "" {
		return nil
	}
	dir, err := resolveHarness(claudeHarnessArg)
	if err != nil {
		return err
	}
	claudeHarness, err = loadHarness(dir)
	return err
}

// applyHarness is the harness step of starting a box: a no-op without
// --harness.
func applyHarness(ctx context.Context, box *boxSSH) error {
	if claudeHarness == nil {
		return nil
	}
	return claudeHarness.apply(ctx, box)
}

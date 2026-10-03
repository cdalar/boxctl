package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// copyProject puts dir on the box at the same absolute path, as a
// gzipped tar streamed over ssh into `tar -x` there.
//
// In a git repository the files are what git itself considers part of
// the project -- `git ls-files --cached --others --exclude-standard`:
// tracked files as they are now (uncommitted changes included) and
// untracked ones that aren't ignored -- plus .git, so the box has the
// history, branches and remotes. Nothing is translated, so ignore rules
// mean exactly what they mean to git. Outside a repository it's the
// whole directory.
func copyProject(ctx context.Context, box *boxSSH, dir string) error {
	files, err := projectFiles(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Copying %s to %s (%d files)...\n", dir, box.name, len(files))
	return streamTar(ctx, box, "mkdir -p "+shellQuote(dir)+" && tar -xzf - -C "+shellQuote(dir),
		func(tw *tar.Writer) error { return addFiles(tw, dir, files) })
}

// projectFiles lists dir's files to copy, as slash-separated paths
// relative to it, sorted.
func projectFiles(dir string) ([]string, error) {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		// Not a repository: everything.
		return walkFiles(dir, "")
	}
	seen := map[string]bool{}
	var files []string
	add := func(rel string) {
		if rel != "" && !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		st, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			// Deleted in the working tree but not yet in the index: the
			// box gets the working tree, so it's gone there too.
			continue
		case st.IsDir():
			// A submodule: its whole checkout.
			sub, err := walkFiles(dir, rel)
			if err != nil {
				return nil, err
			}
			for _, s := range sub {
				add(s)
			}
		default:
			add(rel)
		}
	}
	gitFiles, err := walkFiles(dir, ".git")
	if err != nil {
		return nil, err
	}
	for _, f := range gitFiles {
		add(f)
	}
	sort.Strings(files)
	return files, nil
}

// walkFiles lists every file and symlink under dir/sub (sub "" for dir
// itself), relative to dir, skipping macOS's .DS_Store.
func walkFiles(dir, sub string) ([]string, error) {
	var files []string
	root := filepath.Join(dir, filepath.FromSlash(sub))
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	return files, err
}

// addFiles writes files (relative to dir) into tw: regular files with
// their contents and modes, symlinks as links, owned by root on the box.
// Parent directories are created by tar -x as it goes.
func addFiles(tw *tar.Writer, dir string, files []string) error {
	for _, rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Lstat(full)
		if err != nil {
			continue // gone since it was listed
		}
		link := ""
		if st.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(full); err != nil {
				return err
			}
		} else if !st.Mode().IsRegular() {
			continue // sockets, fifos: nothing to copy
		}
		hdr, err := tar.FileInfoHeader(st, link)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "root", "root"
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if st.Mode().IsRegular() {
			f, err := os.Open(full)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
	}
	return nil
}

// streamTar runs remote on the box with a gzipped tar, written by fill,
// as its stdin.
func streamTar(ctx context.Context, box *boxSSH, remote string, fill func(*tar.Writer) error) error {
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		tw := tar.NewWriter(gz)
		err := fill(tw)
		if err == nil {
			err = tw.Close()
		}
		if err == nil {
			err = gz.Close()
		}
		pw.CloseWithError(err)
	}()
	err := box.run(ctx, remote, pr)
	_ = pr.Close()
	return err
}

// claudeConfigEntries are the parts of ~/.claude that shape how Claude
// works and mean the same thing on the box. Not credentials, history,
// projects/ or plugins: those are this machine's.
var claudeConfigEntries = []string{"CLAUDE.md", "agents", "skills", "commands"}

// claudeSettingsDropped are the settings.json keys that point at this
// machine -- programs to run, plugins installed here -- or hold its
// secrets (env), so they'd break or leak on the box.
var claudeSettingsDropped = []string{
	"hooks", "statusLine", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport",
	"otelHeadersHelper", "enabledPlugins", "extraKnownMarketplaces", "env",
}

// copyClaudeConfig copies the user-level Claude configuration to the
// box's ~/.claude, every time, so the box follows edits made here.
func copyClaudeConfig(ctx context.Context, box *boxSSH) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	src := filepath.Join(home, ".claude")
	var files []string
	for _, entry := range claudeConfigEntries {
		sub, err := walkFiles(src, entry)
		if err != nil {
			return err
		}
		files = append(files, sub...)
	}
	settings, err := boxSettings(filepath.Join(src, "settings.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 && settings == nil {
		return nil
	}
	return streamTar(ctx, box, "mkdir -p ~/.claude && tar -xzf - -C ~/.claude", func(tw *tar.Writer) error {
		if err := addFiles(tw, src, files); err != nil {
			return err
		}
		if settings == nil {
			return nil
		}
		hdr := &tar.Header{Name: "settings.json", Mode: 0o644, Size: int64(len(settings)), Typeflag: tar.TypeReg, Uname: "root", Gname: "root"}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(settings)
		return err
	})
}

// boxSettings is settings.json without claudeSettingsDropped, or nil
// when there's no settings.json.
func boxSettings(p string) ([]byte, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for _, k := range claudeSettingsDropped {
		delete(settings, k)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

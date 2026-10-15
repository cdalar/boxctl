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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"golang.org/x/term"
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
	if err := streamTar(ctx, box, "mkdir -p "+shellQuote(dir)+" && tar -xzf - -C "+shellQuote(dir), nil,
		func(tw *tar.Writer) error { return addFiles(tw, dir, files) }); err != nil {
		return err
	}
	sayDone("Copied %s %s", bold(dir), dim(fmt.Sprintf("(%d files)", len(files))))
	return nil
}

// cloneURL is the URL the box clones the project from: its origin, with
// GitHub over https so the box's credential helper answers for it -- or
// "" when dir isn't a git repository with an origin, and is copied.
func cloneURL(dir string) string {
	if repo := githubRepo(dir); repo != "" {
		return "https://github.com/" + repo + ".git"
	}
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cloneProject clones url's default branch to dir on the box -- the same
// absolute path as here -- and starts branch from it, for Claude to work
// on. Nothing local goes: not uncommitted changes, not unpushed commits.
// With --github forward, your GitHub token is lent for the clone, as it
// is while you're attached.
func cloneProject(ctx context.Context, box *boxSSH, dir, url, branch string) error {
	script := cloneScript(url, dir, branch)
	var err error
	if claudeGitHub == githubForward {
		err = box.runForwarded(ctx, script, boxGitHubSocket)
	} else {
		err = box.run(ctx, script, nil)
	}
	if err != nil {
		return fmt.Errorf("cloning %s on %s: %w", url, box.name, err)
	}
	sayDone("Cloned %s %s", bold(strings.TrimSuffix(strings.TrimPrefix(url, "https://github.com/"), ".git")), dim("on branch "+branch))
	return nil
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
// as its stdin -- counted by progress as it's written, unless nil. The
// pipe to ssh doesn't buffer, so what's written is what's been sent.
func streamTar(ctx context.Context, box *boxSSH, remote string, progress *uploadProgress, fill func(*tar.Writer) error) error {
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		var w io.Writer = gz
		if progress != nil {
			w = io.MultiWriter(gz, progress)
		}
		tw := tar.NewWriter(w)
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
	if progress != nil {
		progress.finish(err == nil)
	}
	return err
}

// uploadProgress draws a progress bar on stderr for an upload of total
// bytes, counted by its Write -- only on a terminal, where \r redraws
// the line; elsewhere it's silent.
type uploadProgress struct {
	label     string
	total     int64
	done      int64
	lastPrint time.Time
	show      bool
}

func newUploadProgress(label string, total int64) *uploadProgress {
	return &uploadProgress{label: label, total: total, show: term.IsTerminal(int(os.Stderr.Fd()))}
}

func (p *uploadProgress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	// Under a spinner the progress is its text, not a bar of its own.
	if sp := currentSpinner(); sp != nil {
		if time.Since(p.lastPrint) >= 200*time.Millisecond {
			sp.setMsg(p.spinText())
			p.lastPrint = time.Now()
		}
		return len(b), nil
	}
	if p.show && time.Since(p.lastPrint) >= 200*time.Millisecond {
		fmt.Fprint(os.Stderr, "\r\033[K"+p.line())
		p.lastPrint = time.Now()
	}
	return len(b), nil
}

// finish draws the bar a last time, full when the upload succeeded, and
// ends its line so what's printed next doesn't land on it.
func (p *uploadProgress) finish(ok bool) {
	if currentSpinner() != nil || !p.show {
		return
	}
	if ok {
		p.done = p.total
	}
	fmt.Fprintln(os.Stderr, "\r\033[K"+p.line())
}

// spinText is the progress as a spinner's text: "label 42% (2.1 MB / 5.0 MB)".
func (p *uploadProgress) spinText() string {
	done := min(p.done, p.total)
	pct := 100.0
	if p.total > 0 {
		pct = float64(done) / float64(p.total) * 100
	}
	return fmt.Sprintf("%s %s %s", p.label, bold(fmt.Sprintf("%.0f%%", pct)),
		dim(fmt.Sprintf("(%s / %s)", client.FormatBytes(done), client.FormatBytes(p.total))))
}

// line is the bar: label, [####----], bytes sent of total, percentage.
// done counts tar headers too, so it can pass total a little: capped.
func (p *uploadProgress) line() string {
	const width = 24
	done := min(p.done, p.total)
	frac := 1.0
	if p.total > 0 {
		frac = float64(done) / float64(p.total)
	}
	filled := int(frac * width)
	return fmt.Sprintf("%s [%s%s] %s / %s %3.0f%%", p.label,
		strings.Repeat("#", filled), strings.Repeat("-", width-filled),
		client.FormatBytes(done), client.FormatBytes(p.total), frac*100)
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
	files, size, err := configFiles(src)
	if err != nil {
		return err
	}
	settings, err := boxSettings(filepath.Join(src, "settings.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 && settings == nil {
		return nil
	}
	total := size + int64(len(settings))
	progress := newUploadProgress("Copying your Claude configuration", total)
	err = streamTar(ctx, box, "mkdir -p ~/.claude && tar -xzf - -C ~/.claude", progress, func(tw *tar.Writer) error {
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
	if err == nil {
		sayDone("Copied your Claude configuration %s", dim("("+client.FormatBytes(total)+")"))
	}
	return err
}

// configSkippedDirs are directories under claudeConfigEntries that are
// this machine's build output or history, not configuration: a skill's
// node_modules or checkout can be hundreds of MB, and uploading it
// every run is what made starting slow.
var configSkippedDirs = map[string]bool{"node_modules": true, ".git": true}

// configFiles lists the files of claudeConfigEntries under src, relative
// to it, without configSkippedDirs or .DS_Store, and their total size.
func configFiles(src string) ([]string, int64, error) {
	var (
		files []string
		size  int64
	)
	for _, entry := range claudeConfigEntries {
		err := filepath.WalkDir(filepath.Join(src, entry), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if configSkippedDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() == ".DS_Store" {
				return nil
			}
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	return files, size, nil
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

// sessionIDPattern is what a Claude Code session ID looks like (a UUID),
// checked before it goes into a path or a glob.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{7,63}$`)

// claudeProjectKey is the directory name Claude Code keeps a project's
// sessions under in ~/.claude/projects: its absolute path with every
// character but letters and digits turned into "-"
// (/Users/me/.src/app -> -Users-me--src-app). The project has the same
// path on the box, so the box's Claude looks for its sessions under the
// same key.
func claudeProjectKey(dir string) string {
	return projectKeyUnsafe.ReplaceAllString(dir, "-")
}

var projectKeyUnsafe = regexp.MustCompile(`[^A-Za-z0-9]`)

// findTranscript locates session id's transcript under projects (the
// local ~/.claude/projects), whichever project key it's under -- a
// session started in a subdirectory has its own -- the newest if more
// than one.
func findTranscript(projects, id string) (string, error) {
	if !sessionIDPattern.MatchString(id) {
		return "", fmt.Errorf("%q isn't a Claude Code session ID", id)
	}
	matches, err := filepath.Glob(filepath.Join(projects, "*", id+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no transcript for session %s under %s", id, projects)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, _ := os.Stat(matches[i])
		b, _ := os.Stat(matches[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	return matches[0], nil
}

// copyTranscript copies session id's transcript (jsonl, from
// findTranscript) -- and its directory of
// subagent transcripts and saved tool results, when it has one -- into
// the box's ~/.claude/projects under the project's key there, so
// `claude --resume <id>` on the box picks the conversation up. The
// session's local rewind checkpoints don't come along.
func copyTranscript(ctx context.Context, box *boxSSH, dir, jsonl, id string) error {
	src := filepath.Dir(jsonl)
	files := []string{id + ".jsonl"}
	sub, err := walkFiles(src, id)
	if err != nil {
		return err
	}
	files = append(files, sub...)
	key := claudeProjectKey(dir)
	fmt.Fprintf(os.Stderr, "Copying this session (%s) to %s...\n", id, box.name)
	return streamTar(ctx, box, "mkdir -p ~/.claude/projects/"+shellQuote(key)+" && tar -xzf - -C ~/.claude/projects/"+shellQuote(key), nil,
		func(tw *tar.Writer) error { return addFiles(tw, src, files) })
}

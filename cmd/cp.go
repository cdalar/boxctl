package cmd

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// `boxctl cp` copies files between this machine and a box with the
// system's scp (the sftp protocol), run through the same ProxyCommand (`boxctl
// ssh-proxy`) and key as `claude fetch`/`push` (boxSSH). Nothing goes
// through the one-shot exec path, so there is no timeout and no size limit.

var cpRecursive bool

var cpCmd = &cobra.Command{
	Use:               "cp [-r] <box>:<path> <local> | <local> <box>:<path>",
	ValidArgsFunction: completeCpArg,
	Short:             "Copy files between a box and this machine",
	Long: `Copies a file, or with -r a directory, from a box to this machine or the
other way round, over real ssh through boxctl's tunnel, with scp. There is no size limit
short of the disk, and a terminal shows progress. A paused box is resumed
first. The first time, cp installs boxctl's ssh key (the one boxctl claude
uses, ~/.boxctl/claude/id_ed25519) in the box's authorized_keys.

  boxctl cp my-box:/root/evidence/shot.png .
  boxctl cp -r my-box:/root/out ./out
  boxctl cp -r ./site my-box:/srv/

Exactly one side is a box: <box>:<path>. A path with a "/" before its first
":" (./a:b) is local, as is a Windows path like C:\\out. Like scp -r, a directory is copied into the target
if that exists; "dir/." copies a directory's contents.`,
	Args: cobra.ExactArgs(2),
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		plan, err := parseCp(args[0], args[1], cpRecursive)
		if err != nil {
			return err
		}
		box, err := connectCpBox(cmd.Context(), plan.box, plan.box+":"+plan.remote)
		if err != nil {
			return err
		}
		defer box.close()
		return plan.run(cmd.Context(), box)
	}),
}

func init() {
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "copy a directory and everything in it")
	rootCmd.AddCommand(cpCmd)
}

// cpPlan is one copy: which box, and which way.
type cpPlan struct {
	box       string
	remote    string // path on the box
	local     string
	upload    bool
	recursive bool
}

// isWinDrive reports whether s starts with a Windows drive, "C:" alone or
// followed by a slash or backslash.
func isWinDrive(s string) bool {
	if len(s) < 2 || s[1] != ':' || (s[0]|0x20 < 'a' || s[0]|0x20 > 'z') {
		return false
	}
	return len(s) == 2 || s[2] == '\\' || s[2] == '/'
}

// splitBoxPath splits "box:path". A local path is anything without a
// colon, with a "/" or "\\" before the first one, with an empty side, or
// (on Windows) a drive path.
func splitBoxPath(s string, windows bool) (box, path string, remote bool) {
	i := strings.Index(s, ":")
	if i <= 0 || i == len(s)-1 || strings.ContainsAny(s[:i], `/\\`) || (windows && isWinDrive(s)) {
		return "", s, false
	}
	return s[:i], s[i+1:], true
}

func parseCp(src, dst string, recursive bool) (*cpPlan, error) {
	win := runtime.GOOS == "windows"
	sb, sp, sRemote := splitBoxPath(src, win)
	db, dp, dRemote := splitBoxPath(dst, win)
	switch {
	case sRemote && dRemote:
		return nil, errors.New("both sides are on boxes -- copy to this machine first; one side must be local")
	case !sRemote && !dRemote:
		return nil, errors.New("neither side is a box -- name one as <box>:<path>")
	case sRemote:
		return &cpPlan{box: sb, remote: sp, local: dp, recursive: recursive}, nil
	default:
		return &cpPlan{box: db, remote: dp, local: sp, upload: true, recursive: recursive}, nil
	}
}

func completeCpArg(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if strings.Contains(toComplete, ":") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, _ := completeBoxName(cmd, nil, toComplete)
	for i := range names {
		names[i] += ":"
	}
	return names, cobra.ShellCompDirectiveNoSpace | cobra.ShellCompDirectiveDefault
}

func (p *cpPlan) run(ctx context.Context, box *boxSSH) error {
	if p.upload {
		st, err := os.Stat(strings.TrimSuffix(p.local, "/."))
		if err != nil {
			return err
		}
		if st.IsDir() && !p.recursive {
			return fmt.Errorf("%s is a directory -- use -r", p.local)
		}
	} else if !p.recursive && box.run(ctx, "test -d "+shellQuote(p.remote), nil) == nil {
		return fmt.Errorf("%s:%s is a directory -- use -r", p.box, p.remote)
	}
	return box.copy(ctx, p)
}

// sshOpts is b.args without the destination, for scp.
func (b *boxSSH) sshOpts() []string { return b.args[:len(b.args)-1] }

// localArg makes a local path safe to hand to scp: one that starts with
// "-" would be an option, and one with a colon before any "/" would be a
// host:path. "./" in front makes either a plain path. (A Windows drive
// letter is how scp there expects a local path, so it stays as is.)
func localArg(s string, windows bool) string {
	if windows && isWinDrive(s) {
		return s
	}
	if strings.HasPrefix(s, "-") {
		return "./" + s
	}
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		return "./" + s
	}
	return s
}

// scpArgs is the whole argument vector of the scp that does a copy.
func (b *boxSSH) scpArgs(p *cpPlan, quiet, windows bool) []string {
	args := append([]string{"-o", "BatchMode=yes"}, b.sshOpts()...)
	if p.recursive {
		args = append(args, "-r")
	}
	if quiet {
		args = append(args, "-q")
	}
	remote := b.args[len(b.args)-1] + ":" + p.remote
	local := localArg(p.local, windows)
	if p.upload {
		return append(args, "--", local, remote)
	}
	return append(args, "--", remote, local)
}

func (b *boxSSH) copy(ctx context.Context, p *cpPlan) error {
	if _, err := exec.LookPath("scp"); err != nil {
		return errors.New("boxctl cp needs scp on this machine")
	}
	cmd := exec.CommandContext(ctx, "scp", b.scpArgs(p, !isTTY(), runtime.GOOS == "windows")...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copying with scp: %w", err)
	}
	return nil
}

// connectCpBox is connectClaudeBox with cp's own words for what can go
// wrong first. arg is the <box>:<path> as typed, for the hint.
func connectCpBox(ctx context.Context, name, arg string) (*boxSSH, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, errors.New("boxctl cp needs ssh on this machine")
	}
	vms, err := newClient().List(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, vm := range vms {
		found = found || vm.Name == name
	}
	if !found {
		msg := fmt.Sprintf("no box %q -- boxctl ls lists yours", name)
		if arg != "" {
			msg += fmt.Sprintf(" (for a local file named %s, write ./%s)", arg, arg)
		}
		return nil, errors.New(msg)
	}
	box, _, err := connectClaudeBox(ctx, name, "", false, false)
	return box, err
}

// --- boxctl claude evidence -----------------------------------------------

var boxEvidenceDir = "/root/evidence"

var claudeEvidenceCmd = &cobra.Command{
	Use:               "evidence <box> [dir]",
	ValidArgsFunction: completeBoxName,
	Short:             "Fetch a run's /root/evidence/ from its box",
	Long: `Copies the box's /root/evidence/ -- the screenshots and recordings a run
leaves there -- into ./evidence/<box>/ (or dir), and lists what arrived with
sizes. A box with no /root/evidence says so and exits non-zero.

The files come as a tar stream over ssh into a fresh directory that then
replaces ./evidence/<box>/ (or a dir that is empty or an earlier fetch).
Symbolic links and other special files are skipped, never followed, so
nothing outside /root/evidence is copied. Like cp, the first use installs
boxctl's ssh key on the box.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		dest, own := filepath.Join("evidence", args[0]), true
		if len(args) == 2 {
			dest, own = args[1], false
		}
		box, err := connectCpBox(cmd.Context(), args[0], "")
		if err != nil {
			return err
		}
		defer box.close()
		return fetchEvidence(cmd.Context(), box, args[0], dest, own)
	}),
}

func init() { claudeCmd.AddCommand(claudeEvidenceCmd) }

// evidenceMarker marks a directory as an earlier evidence fetch, which a
// later one may replace.
const evidenceMarker = ".boxctl-evidence"

// fetchEvidence tars boxEvidenceDir on the box (tar doesn't follow
// symlinks), unpacks it into a fresh directory beside dest, and moves that
// into place. own says dest is the default ./evidence/<box>, which is
// always replaced; another dir only if it's empty or an earlier fetch.
func fetchEvidence(ctx context.Context, box *boxSSH, name, dest string, own bool) error {
	if box.run(ctx, "test -d "+boxEvidenceDir, nil) != nil {
		return fmt.Errorf("%s has no %s", name, boxEvidenceDir)
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if cwd, err := os.Getwd(); err == nil && (cwd == absDest || strings.HasPrefix(cwd, absDest+string(filepath.Separator))) {
		return fmt.Errorf("%s holds the current directory -- name a directory of its own", dest)
	}
	_, statErr := os.Stat(dest)
	exists := statErr == nil
	replace := exists
	if exists && !own {
		if ents, _ := os.ReadDir(dest); len(ents) > 0 {
			if _, err := os.Stat(filepath.Join(dest, evidenceMarker)); err != nil {
				return fmt.Errorf("%s exists and isn't an earlier evidence fetch -- pick another directory or remove it", dest)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(absDest), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(absDest), ".evidence-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(box.streamTo(ctx, "tar -C "+boxEvidenceDir+" -cf - .", pw))
	}()
	files, skipped, extractErr := extractTar(pr, tmp)
	_, _ = io.Copy(io.Discard, pr) // let ssh finish
	_ = pr.Close()
	for _, s := range skipped {
		sayWarn("skipped %s", s)
	}
	if extractErr != nil && len(files) == 0 {
		return fmt.Errorf("fetching %s's %s: %w", name, boxEvidenceDir, extractErr)
	}
	if extractErr == nil && len(files) == 0 {
		return fmt.Errorf("%s's %s is empty", name, boxEvidenceDir)
	}
	if err := os.WriteFile(filepath.Join(tmp, evidenceMarker), nil, 0o644); err != nil {
		return err
	}
	if replace {
		sayNote("replacing the earlier contents of %s", dest)
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return err
	}
	if extractErr == nil {
		sayDone("Fetched %d files from %s into %s", len(files), bold(name), dest)
	} else {
		sayWarn("only part of %s's evidence arrived: %d files in %s", name, len(files), dest)
	}
	for _, f := range files {
		say(fmt.Sprintf("    %10s  %s", humanBytes(f.size), f.path))
	}
	if extractErr != nil {
		return fmt.Errorf("fetching %s's %s: %w", name, boxEvidenceDir, extractErr)
	}
	return nil
}

// extractTar unpacks regular files and directories from a tar stream into
// root. An entry whose name would leave root is an error; symlinks, hard
// links and devices are skipped and returned as notes. files is what was
// written, sorted, even when err says the stream broke off.
func extractTar(r io.Reader, root string) (files []fileSize, skipped []string, err error) {
	tr := tar.NewReader(r)
	defer func() { sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path }) }()
	for {
		h, herr := tr.Next()
		if herr == io.EOF {
			return files, skipped, nil
		}
		if herr != nil {
			return files, skipped, herr
		}
		rel := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if rel == "." {
			continue
		}
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, `\`) || rel == evidenceMarker {
			return files, skipped, fmt.Errorf("refusing tar entry %q: outside the destination", h.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, skipped, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, skipped, err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return files, skipped, err
			}
			n, cerr := io.Copy(f, tr)
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return files, skipped, cerr
			}
			files = append(files, fileSize{rel, n})
		default:
			skipped = append(skipped, fmt.Sprintf("%s (not a regular file; links are not followed)", rel))
		}
	}
}

type fileSize struct {
	path string
	size int64
}

// listFiles lists the regular files under dir, relative to it, sorted.
func listFiles(dir string) ([]fileSize, error) {
	var out []fileSize
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == evidenceMarker {
			return nil
		}
		out = append(out, fileSize{rel, info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, err
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

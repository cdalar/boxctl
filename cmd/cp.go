package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// `boxctl cp` copies files between this machine and a box with the
// system's rsync or scp, run through the same ProxyCommand (`boxctl
// ssh-proxy`) and key as `claude fetch`/`push` (boxSSH). Nothing goes
// through the one-shot exec path, so there is no timeout and no size limit.

var cpRecursive bool

var cpCmd = &cobra.Command{
	Use:               "cp [-r] <box>:<path> <local> | <local> <box>:<path>",
	ValidArgsFunction: completeCpArg,
	Short:             "Copy files between a box and this machine",
	Long: `Copies a file, or with -r a directory, from a box to this machine or the
other way round, over real ssh through boxctl's tunnel: rsync when both
sides have it, else scp. There is no size limit short of the disk, and a
terminal shows progress. A paused box is resumed first.

  boxctl cp my-box:/root/evidence/shot.png .
  boxctl cp -r my-box:/root/out ./out
  boxctl cp -r ./site my-box:/srv/

Exactly one side is a box: <box>:<path>. A path with a "/" before its first
":" (./a:b) is local. Like scp -r, a directory is copied into the target
if that exists; "dir/." copies a directory's contents.`,
	Args: cobra.ExactArgs(2),
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		plan, err := parseCp(args[0], args[1], cpRecursive)
		if err != nil {
			return err
		}
		box, _, err := connectClaudeBox(cmd.Context(), plan.box, "", false, false)
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

// splitBoxPath splits "box:path". A local path is anything without a
// colon, with a "/" before the first one, or with an empty side.
func splitBoxPath(s string) (box, path string, remote bool) {
	i := strings.Index(s, ":")
	if i <= 0 || i == len(s)-1 || strings.Contains(s[:i], "/") {
		return "", s, false
	}
	return s[:i], s[i+1:], true
}

func parseCp(src, dst string, recursive bool) (*cpPlan, error) {
	sb, sp, sRemote := splitBoxPath(src)
	db, dp, dRemote := splitBoxPath(dst)
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

// remoteHasRsync reports whether rsync is on both this machine and the box.
func (b *boxSSH) remoteHasRsync(ctx context.Context) bool {
	if _, err := exec.LookPath("rsync"); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "ssh", append(append([]string{"-o", "BatchMode=yes"}, b.args...), "command -v rsync >/dev/null")...).Run() == nil
}

// isDir and exists answer for a copy's source or target, on whichever
// side it is: arg is p.local or the host:path form of p.remote.
func (b *boxSSH) isDir(ctx context.Context, p *cpPlan, arg string) bool {
	if p.upload {
		st, err := os.Stat(arg)
		return err == nil && st.IsDir()
	}
	return b.run(ctx, "test -d "+shellQuote(p.remote), nil) == nil
}

func (b *boxSSH) exists(ctx context.Context, p *cpPlan, arg string) bool {
	if p.upload {
		return b.run(ctx, "test -e "+shellQuote(p.remote), nil) == nil
	}
	_, err := os.Stat(arg)
	return err == nil
}

// sshOpts is b.args without the destination, for scp and rsync -e.
func (b *boxSSH) sshOpts() []string { return b.args[:len(b.args)-1] }

func (b *boxSSH) copy(ctx context.Context, p *cpPlan) error {
	var cmd *exec.Cmd
	host := b.args[len(b.args)-1]
	if b.remoteHasRsync(ctx) {
		e := []string{"ssh", "-o", "BatchMode=yes"}
		for _, a := range b.sshOpts() {
			e = append(e, shellQuote(a))
		}
		args := []string{"-e", strings.Join(e, " ")}
		if p.recursive {
			args = append(args, "-rltp")
		} else {
			args = append(args, "-lt")
		}
		if isTTY() {
			args = append(args, "--info=progress2")
		}
		// -s: the remote path reaches rsync as is, so spaces need no
		// quoting (which rsync 3.2.4+ would then take literally).
		args = append(args, "-s")
		remote := host + ":" + p.remote
		src, dst := p.local, remote
		if !p.upload {
			src, dst = remote, p.local
		}
		// scp -r (and cp -r) make a directory that doesn't exist yet the
		// copy; rsync would put the copy inside it. A trailing slash
		// makes rsync agree.
		if p.recursive && !strings.HasSuffix(src, "/") && !strings.HasSuffix(src, "/.") && b.isDir(ctx, p, src) && !b.exists(ctx, p, dst) {
			src += "/"
		}
		args = append(args, src, dst)
		cmd = exec.CommandContext(ctx, "rsync", args...)
	} else {
		if _, err := exec.LookPath("scp"); err != nil {
			return errors.New("boxctl cp needs rsync or scp on this machine")
		}
		args := append([]string{"-o", "BatchMode=yes"}, b.sshOpts()...)
		if p.recursive {
			args = append(args, "-r")
		}
		if !isTTY() {
			args = append(args, "-q")
		}
		remote := host + ":" + p.remote
		if p.upload {
			args = append(args, p.local, remote)
		} else {
			args = append(args, remote, p.local)
		}
		cmd = exec.CommandContext(ctx, "scp", args...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("copying with %s: %w", cmd.Args[0], err)
	}
	return nil
}

// --- boxctl claude evidence -----------------------------------------------

const boxEvidenceDir = "/root/evidence"

var claudeEvidenceCmd = &cobra.Command{
	Use:               "evidence <box> [dir]",
	ValidArgsFunction: completeBoxName,
	Short:             "Fetch a run's /root/evidence/ from its box",
	Long: `Copies the box's /root/evidence/ -- the screenshots and recordings a run
leaves there -- into ./evidence/<box>/ (or dir), and lists what arrived with
sizes. A box with no /root/evidence says so and exits non-zero.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: claudeAgent.runs(func(cmd *cobra.Command, args []string) error {
		dest := filepath.Join("evidence", args[0])
		if len(args) == 2 {
			dest = args[1]
		}
		return fetchEvidence(cmd.Context(), args[0], dest)
	}),
}

func init() { claudeCmd.AddCommand(claudeEvidenceCmd) }

func fetchEvidence(ctx context.Context, name, dest string) error {
	box, _, err := connectClaudeBox(ctx, name, "", false, false)
	if err != nil {
		return err
	}
	defer box.close()
	if box.run(ctx, "test -d "+boxEvidenceDir, nil) != nil {
		return fmt.Errorf("%s has no %s", name, boxEvidenceDir)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	// "/." copies the directory's contents, not the directory.
	p := &cpPlan{box: name, remote: boxEvidenceDir + "/.", local: dest, recursive: true}
	if err := box.copy(ctx, p); err != nil {
		return err
	}
	files, err := listFiles(dest)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s's %s is empty", name, boxEvidenceDir)
	}
	sayDone("Fetched %d files from %s into %s", len(files), bold(name), dest)
	for _, f := range files {
		say(fmt.Sprintf("    %10s  %s", humanBytes(f.size), f.path))
	}
	return nil
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

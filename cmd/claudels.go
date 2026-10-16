package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

// lsCmd is an agent's ls subcommand.
func (a *boxAgent) lsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: a.fill("List this project's {Agent} sessions: the main one and its tasks"),
		Long: a.fill(`Lists the {Agent} sessions working on this project: the main checkout and
each --task worktree on the project's box (with its branch, whether
{Agent} is running there, and when it last showed activity), plus the
boxes of --own-box tasks. Then the run boxes: the project's --repo and
ephemeral runs, and every --goal/--issue run (goal-... boxes) whatever
its project, with how a goal went and its pull request. A box
whose goal has finished and that nothing has used for 10 minutes is
paused.`),
		Args: cobra.NoArgs,
		RunE: a.runs(func(cmd *cobra.Command, _ []string) error {
			return claudeList(cmd.Context())
		}),
	}
}

func init() {
	claudeCmd.AddCommand(claudeAgent.lsCmd())
}

func claudeList(ctx context.Context) error {
	dir, err := projectDir()
	if err != nil {
		return err
	}
	main := claudeBox
	if main == "" {
		main = claudeBoxName(dir)
	}
	vms, err := newClient().List(ctx)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "TASK\tBRANCH\t%s\tACTIVE\tBOX\n", strings.ToUpper(agent.name))
	// The project's box first, then the --own-box tasks' boxes.
	sort.SliceStable(vms, func(i, j int) bool { return vms[i].Name == main && vms[j].Name != main })
	rows := 0
	// Run boxes hold the project at its own path, or a --repo/--goal clone
	// in /root/<repository> -- or, among others, in the workspace.
	prefixes, projects := []string{filepath.Base(dir)}, map[string]bool{dir: true}
	if repo := githubRepo(dir); repo != "" {
		name := path.Base(repo)
		prefixes = append(prefixes, name, "workspace")
		projects[path.Join("/root", name)] = true
		projects[path.Join(boxWorkspace, name)] = true
	}
	var runs []client.VM
	for _, vm := range vms {
		switch {
		case vm.Name == main && vm.State == "running":
			n, err := listBoxSessions(ctx, tw, dir)
			if err != nil {
				return err
			}
			rows += n
		case vm.Name == main:
			_, _ = fmt.Fprintf(tw, "(all)\t-\t-\t-\t%s (%s)\n", vm.Name, vm.State)
			rows++
		case strings.HasPrefix(vm.Name, main+"-"):
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t%s (own box, %s)\n", strings.TrimPrefix(vm.Name, main+"-"), vm.Name, vm.State)
			rows++
		case isGoalBox(vm.Name), isRunBox(vm.Name, prefixes):
			runs = append(runs, vm)
		}
	}
	boxes := map[string]bool{}
	for _, vm := range vms {
		boxes[vm.Name] = true
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })
	runTable := listRuns(ctx, runs, projects, boxes)
	if rows == 0 && runTable == "" {
		fmt.Fprintf(os.Stderr, "No %s sessions for %s -- start one with boxctl %s.\n", agent.title, dir, agent.name)
		return nil
	}
	if rows > 0 {
		if err := tw.Flush(); err != nil {
			return err
		}
		if runTable != "" {
			fmt.Println()
		}
	}
	fmt.Print(runTable)
	return nil
}

// listRuns is a table with a row per run box of the project's, and every
// goal run's whatever its project ("" for none), reading each
// running one's goal status and pausing it if the goal is over and nothing
// uses the box. The name only says which project a box might be for (a
// boxctl-web box's starts with boxctl-, too); a running box's project
// marker says which it is, and a paused one's, if ls has seen it, is in
// the cache.
func listRuns(ctx context.Context, runs []client.VM, projects, boxes map[string]bool) string {
	cached := loadGoalCache()
	read := make([]*goalStatus, len(runs))
	project := make([][]string, len(runs))
	errs := make([]error, len(runs))
	var fns []func()
	for i, vm := range runs {
		if vm.State != "running" {
			continue
		}
		fns = append(fns, func() {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			box, err := connectClaudeBox(ctx, vm.Name, false, false)
			if err != nil {
				errs[i] = err
				return
			}
			defer box.close()
			out, err := box.output(ctx, runInfoScript)
			if err != nil {
				errs[i] = err
				return
			}
			project[i], read[i], errs[i] = parseRunInfo(out)
		})
	}
	inParallel(fns...)

	fresh := map[string]goalStatus{}
	c := newClient()
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "RUN BOX\tSTATE\tGOAL\tUPDATED\tBRANCH\tPULL REQUEST")
	rows := 0
	for i, vm := range runs {
		if s, ok := cached[vm.Name]; ok && vm.State != "running" {
			project[i] = s.Projects
		}
		if !isGoalBox(vm.Name) && !ofProject(project[i], projects) {
			continue
		}
		rows++
		state, goal, updated, branch, pr := vm.State, "-", "-", "-", "-"
		st := read[i]
		switch {
		case errs[i] != nil:
			goal = "?"
			fmt.Fprintf(os.Stderr, "warning: couldn't read %s's goal: %v\n", vm.Name, errs[i])
		case st == nil && vm.State == "running":
			// No goal; remembered for its project.
			fresh[vm.Name] = goalStatus{Projects: project[i]}
		case st != nil:
			st.Projects = project[i]
			fresh[vm.Name] = *st
			if paused, err := pauseFinishedGoal(ctx, c, &vm, st, false); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			} else if paused {
				state = "paused"
			}
		default: // not running: what ls saw last
			if s, ok := cached[vm.Name]; !ok {
				goal = "?"
			} else if s.Status != "" {
				st = &s
			}
		}
		if st != nil {
			goal = st.Status
			if t, err := time.Parse(time.RFC3339, st.Updated); err == nil {
				updated = ago(t)
			}
			if st.Branch != "" {
				branch = st.Branch
			}
			if st.PRURL != "" {
				pr = st.PRURL
			}
			if n := len(st.PRURLs); n > 1 {
				pr += fmt.Sprintf(" (+%d more)", n-1)
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", vm.Name, state, goal, updated, branch, pr)
	}
	rememberGoals(fresh, boxes)
	if rows == 0 {
		return ""
	}
	_ = tw.Flush()
	return buf.String()
}

// listBoxSessions writes a row per worktree on the project's (running)
// box.
func listBoxSessions(ctx context.Context, tw *tabwriter.Writer, dir string) (int, error) {
	t, err := openClaudeBox(ctx, false, false)
	if err != nil {
		return 0, err
	}
	defer t.box.close()
	if t.needsCopy {
		return 0, nil
	}
	out, err := t.box.output(ctx, "git -C "+shellQuote(dir)+" worktree list --porcelain 2>/dev/null; echo '--tmux--'; tmux ls -F '#{session_name} #{session_activity}' 2>/dev/null || true")
	if err != nil {
		return 0, err
	}
	porcelain, sessions, _ := strings.Cut(string(out), "--tmux--")
	branches := worktreeBranches(porcelain)
	activity := map[string]int64{}
	for _, line := range strings.Split(sessions, "\n") {
		name, at, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(at, 10, 64); err == nil {
			activity[name] = n
		}
	}
	worktrees := parseWorktrees(porcelain, dir)
	sort.SliceStable(worktrees, func(i, j int) bool { return worktrees[i].task < worktrees[j].task })
	for _, w := range worktrees {
		s := newTaskSession(dir, w.task)
		task := w.task
		if task == "" {
			task = "(main)"
		}
		state, active := "stopped", "-"
		if at, ok := activity[s.tmux]; ok {
			state, active = "running", ago(time.Unix(at, 0))
		}
		branch := branches[w.dir]
		if branch == "" {
			branch = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", task, branch, state, active, t.name)
	}
	return len(worktrees), nil
}

// ofProject reports whether a run box with dirs is the project's, whose
// directories on a box are projects. Unknown (no dirs: a paused box ls
// hasn't seen running) counts as yes -- its name already matched.
func ofProject(dirs []string, projects map[string]bool) bool {
	if len(dirs) == 0 {
		return true
	}
	for _, d := range dirs {
		if projects[d] {
			return true
		}
	}
	return false
}

// worktreeBranches maps each worktree's path to its branch, from `git
// worktree list --porcelain`.
func worktreeBranches(porcelain string) map[string]string {
	branches := map[string]string{}
	path := ""
	for _, line := range strings.Split(porcelain, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = p
		} else if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			branches[path] = b
		}
	}
	return branches
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

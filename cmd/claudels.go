package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sort"
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
		Short: a.fill("List this project's {Agent} tasks and boxes, and every goal run"),
		Long: a.fill(`Lists the tasks on the project's box -- the main checkout and each --task
worktree, with its branch, whether {Agent} is running there, and when it
last showed activity (in herdr: what it's doing -- working, blocked, idle)
-- then, as boxctl ls -p -a lists them, the
project's boxes (its own, --own-box tasks', ephemeral and --repo runs')
and every --goal/--issue run whatever its project, with how the goal is
going and its pull request. Only the project's box is connected to, for
its tasks; the rest is from what the boxes report to the server, so a
paused box shows its real status too. Boxes are found by the labels
boxctl gives them; boxctl label adds them to a box from before labels.
The server pauses a box whose goal has finished and that nothing has
used for 10 minutes.`),
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

	// The tasks on the project's box: only it, and only running, is
	// connected to.
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "TASK\tBRANCH\t%s\tACTIVE\tBOX\n", strings.ToUpper(agent.name))
	rows := 0
	if i := slices.IndexFunc(vms, func(vm client.VM) bool { return vm.Name == main }); i >= 0 && vms[i].State == "running" {
		if rows, err = listBoxSessions(ctx, tw, dir); err != nil {
			return err
		}
	}
	if rows > 0 {
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	// Then the boxes, as boxctl ls -p -a lists them, plus every goal
	// run's: a goal needs no checkout to start from, so it's listed
	// wherever ls runs.
	repo := githubRepo(dir)
	vms = slices.DeleteFunc(vms, func(vm client.VM) bool {
		if a := vm.Labels["boxctl.agent"]; a != "" && a != agent.name {
			return true
		}
		return !inProject(vm, dir, repo) && vm.Labels["boxctl.run"] != "goal"
	})
	if len(vms) == 0 {
		fmt.Fprintf(os.Stderr, "No %s sessions for %s -- start one with boxctl %s.\n", agent.title, dir, agent.name)
		return nil
	}
	if rows > 0 {
		fmt.Println()
	}
	return printBoxes(ctx, vms, "table", true, "")
}

// printRunStatus is claude status's report on one run box, from its
// labels and run_status.
func printRunStatus(vm client.VM) {
	goal, updated, branch, pr := runRow(vm)
	if updated != "-" {
		goal += " (" + updated + ")"
	}
	fmt.Printf("Box:     %s (%s)\nRun:     %s\nGoal:    %s\nBranch:  %s\nPR:      %s\n", vm.Name, vm.State, runText(vm), goal, branch, pr)
	if st, _ := parseGoalStatus(vm.RunStatus); st != nil && st.Error != "" {
		fmt.Printf("Error:   %s\n", st.Error)
	}
}

// runRow is a run box's goal, when it last changed, branch and pull request:
// "-" where there's nothing to show, GOAL "?" for a goal box whose status
// can't be told from its run_status.
func runRow(vm client.VM) (goal, updated, branch, pr string) {
	goal, updated, branch, pr = "-", "-", "-", "-"
	if b := vm.Labels["boxctl.branch"]; b != "" {
		branch = b
	}
	st, err := parseGoalStatus(vm.RunStatus)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", vm.Name, err)
		return "?", updated, branch, pr
	case st == nil:
		if vm.Labels["boxctl.run"] == "goal" {
			goal = "?"
		}
		return goal, updated, branch, pr
	}
	if st.Status != "" {
		goal = st.Status
	}
	if t, err := time.Parse(time.RFC3339, st.Updated); err == nil {
		updated = ago(t)
	}
	if st.Branch != "" {
		branch = st.Branch
	}
	// The box wrote it; only a GitHub URL is shown as one.
	if strings.HasPrefix(st.PRURL, "https://github.com/") {
		pr = st.PRURL
	}
	if n := len(st.PRURLs); n > 1 {
		pr += fmt.Sprintf(" (+%d more)", n-1)
	}
	return goal, updated, branch, pr
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
	out, err := t.box.output(ctx, "git -C "+shellQuote(dir)+" worktree list --porcelain 2>/dev/null; echo '--sessions--'; "+mux.list())
	if err != nil {
		return 0, err
	}
	porcelain, sessions, _ := strings.Cut(string(out), "--sessions--")
	branches := worktreeBranches(porcelain)
	activity := mux.parseList(sessions)
	worktrees := parseWorktrees(porcelain, dir)
	sort.SliceStable(worktrees, func(i, j int) bool { return worktrees[i].task < worktrees[j].task })
	for _, w := range worktrees {
		s := newTaskSession(dir, w.task)
		task := w.task
		if task == "" {
			task = "(main)"
		}
		state, active := "stopped", "-"
		if a, ok := activity[s.name]; ok {
			state, active = "running", a
		}
		branch := branches[w.dir]
		if branch == "" {
			branch = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", task, branch, state, active, t.name)
	}
	return len(worktrees), nil
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

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var lsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List your running boxes (-a for all of them)",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, err := outputFormat(cmd)
		if err != nil {
			return err
		}
		labels, _ := cmd.Flags().GetStringArray("label")
		vms, err := newClient().List(cmd.Context(), labels...)
		if err != nil {
			return err
		}
		none := "No boxes yet. Create one with `boxctl create <name>`."
		if len(labels) > 0 {
			none = "No boxes with those labels."
		}
		if project, _ := cmd.Flags().GetBool("project"); project {
			dir, err := projectDir()
			if err != nil {
				return err
			}
			repo := githubRepo(dir)
			vms = slices.DeleteFunc(vms, func(vm client.VM) bool { return !inProject(vm, dir, repo) })
			none = "No boxes for " + dir + "."
		}
		all, _ := cmd.Flags().GetBool("all")
		return printBoxes(cmd.Context(), vms, format, all, none)
	},
}

// printBoxes is ls's listing of vms in format (table, wide or json):
// only the live ones unless all, none when there are no boxes at all.
// On a terminal it then stays around to fill in each PR's state
// (lspr.go), which is the only use it has for ctx.
func printBoxes(ctx context.Context, vms []client.VM, format string, all bool, none string) error {
	asJSON := format == "json"
	if len(vms) == 0 && !asJSON {
		fmt.Println(none)
		return nil
	}

	// Like `docker ps`: only live boxes unless -a. The note about the
	// rest goes to stderr, so stdout stays the listing alone -- after
	// the table, where fillPRStates has to count it as a line.
	var note string
	if !all {
		live := vms[:0]
		for _, vm := range vms {
			if stateRank(vm.State) == 0 {
				live = append(live, vm)
			}
		}
		hidden := len(vms) - len(live)
		vms = live
		if !asJSON {
			if len(vms) == 0 {
				fmt.Println("No running boxes.")
			}
			if hidden > 0 {
				note = fmt.Sprintf("%s not running; `boxctl ls -a` lists them.", boxCount(hidden))
			}
			if len(vms) == 0 {
				if note != "" {
					fmt.Fprintln(os.Stderr, note)
				}
				return nil
			}
		}
	}

	sort.Slice(vms, func(i, j int) bool {
		ri, rj := stateRank(vms[i].State), stateRank(vms[j].State)
		if ri != rj {
			return ri < rj
		}
		if !vms[i].CreatedAt.Equal(vms[j].CreatedAt) {
			return vms[i].CreatedAt.After(vms[j].CreatedAt)
		}
		return vms[i].ID < vms[j].ID
	})
	if asJSON {
		return printJSON(vms)
	}

	// Each box is two lines: the box itself -- name, state, image,
	// size and age, IP, provider and readiness too with -o wide -- in
	// the table's columns, then, indented, what its run is up to
	// (runParts): run, project, branch and the pull request's URL. The
	// table's lines all go through one tabwriter, so columns line up
	// across sections; the run lines (no columns) and the section labels
	// are slotted in afterwards -- either written through the tabwriter
	// would end the column block, and the lines after it would align on
	// their own.
	wide := format == "wide"
	var table strings.Builder
	if wide {
		fmt.Fprintln(&table, "NAME\tSTATE\tIP\tIMAGE\tSIZE\tPROVIDER\tREADY\tAGE")
	} else {
		fmt.Fprintln(&table, "NAME\tSTATE\tIMAGE\tSIZE\tAGE")
	}
	counts := make([]int, len(lsSections))
	for _, vm := range vms {
		counts[stateRank(vm.State)]++
		image := vm.Image
		if image == "" {
			image = "-"
		}
		if !wide {
			fmt.Fprintf(&table, "%s\t%s\t%s\t%s\t%s\n", vm.Name, vm.State, image, sizeLabel(vm), age(vm.CreatedAt))
			continue
		}
		ip := vm.IP
		if ip == "" {
			ip = "-"
		}
		ready := "no"
		if vm.Ready {
			ready = "yes"
		}
		fmt.Fprintf(&table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", vm.Name, vm.State, ip, image, sizeLabel(vm), vm.Provider, ready, age(vm.CreatedAt))
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	if _, err := io.WriteString(w, table.String()); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	header, rows := lines[0], lines[1:]

	// The screen, line by line: plain, for the widths fillPRStates's
	// wrap check needs, and as shown, with colour -- escape codes the
	// widths mustn't count, which is also why colour comes after
	// alignment. prLine is which line each box's run line is, so
	// fillPRStates can find it again.
	var plain, shown []string
	add := func(p, s string) {
		plain = append(plain, p)
		shown = append(shown, s)
	}
	color := useColor()
	if color {
		header = bold(header)
	}
	prLine := make([]int, len(vms))
	cells := make([]prCell, len(vms))
	hasPR := make([]bool, len(vms))
	addBox := func(i int) {
		vm, row := vms[i], rows[i]
		coloured := row
		if n := len(vm.Name); color && len(row) >= n {
			coloured = row[:n] + strings.Replace(row[n:], vm.State, stateText(vm.State), 1)
		}
		add(row, coloured)
		parts, cell, ok := runParts(vm)
		if len(parts) == 0 {
			return
		}
		prLine[i] = len(plain)
		cells[i], hasPR[i] = cell, ok
		add(runLine(parts, false), runLine(parts, color))
	}

	// Like `onctl ls`: with boxes in one state only, a plain table;
	// otherwise a labelled section per state, each under its own
	// header row.
	groups := 0
	for _, n := range counts {
		if n > 0 {
			groups++
		}
	}
	if groups < 2 {
		add(header, header)
		for i := range rows {
			addBox(i)
		}
	} else {
		i := 0
		for rank, n := range counts {
			if n == 0 {
				continue
			}
			if len(plain) > 0 {
				add("", "")
			}
			label := fmt.Sprintf("● %s (%d)", lsSections[rank].label, n)
			if color {
				add(label, lsSections[rank].color+label+ansiReset)
			} else {
				add(label, label)
			}
			add(header, header)
			for range n {
				addBox(i)
				i++
			}
		}
	}
	if _, err := os.Stdout.WriteString(strings.Join(shown, "\n") + "\n"); err != nil {
		return err
	}
	// The note is one more line on the screen when stderr is the
	// terminal too.
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
		if term.IsTerminal(int(os.Stderr.Fd())) {
			plain = append(plain, note)
		}
	}
	// A line the terminal wraps shifts every line above it, so each
	// cell is told the widest line from its own down to the last; the
	// lines above it don't matter.
	widest := make([]int, len(plain)+1)
	for i := len(plain) - 1; i >= 0; i-- {
		widest[i] = max(widest[i+1], utf8.RuneCountInString(plain[i]))
	}
	var prs []prCell
	for i, c := range cells {
		if hasPR[i] {
			c.up = len(plain) - prLine[i]
			c.width = widest[prLine[i]]
			prs = append(prs, c)
		}
	}
	fillPRStates(ctx, prs)
	return nil
}

// runText is ls's RUN column: what made the box and what for, from its
// labels alone (sessionLabels, runLabels) -- "claude goal", "kilo
// session" -- and, for a run that reports one, how it's going from its
// run_status ("claude goal: done"). A goal box without a run_status (an
// image older than boxctl-status) gets "?": ls never reaches into a box
// to find out. "-" for a box nothing labeled.
func runText(vm client.VM) string {
	run := vm.Labels["boxctl.run"]
	if run == "" {
		return "-"
	}
	text := run
	if a := vm.Labels["boxctl.agent"]; a != "" {
		text = a + " " + run
	}
	switch st, err := parseGoalStatus(vm.RunStatus); {
	case err != nil:
		return text + ": ?"
	case st != nil && st.Status != "":
		return text + ": " + st.Status
	case run == "goal":
		return text + ": ?"
	}
	return text
}

// boxCount is "1 box" or "3 boxes".
func boxCount(n int) string {
	if n == 1 {
		return "1 box"
	}
	return fmt.Sprintf("%d boxes", n)
}

// lsSections are `ls`'s sections, indexed by stateRank.
var lsSections = []struct{ label, color string }{
	{"RUNNING", "\033[1;32m"},
	{"PAUSED", "\033[1;33m"},
	{"OTHER", "\033[1;90m"},
}

// useColor reports whether `ls` should colour its section labels: only
// on a terminal, and not when NO_COLOR (https://no-color.org) is set.
func useColor() bool {
	return os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))
}

// stateRank is a box's section in `ls` (an index into lsSections),
// grouped the same way boxctl.io's dashboard does (boxctl-web's
// STATE_RANK): live boxes -- usable, or on their way there
// -- first, then paused ones (one resume away), then anything else
// (stopped, or a state this CLI doesn't know yet). Within a group, ls
// sorts newest first by created_at, which pause/resume never touch, with
// id breaking ties since created_at is only second-precision and the
// server doesn't keep row order stable. Unlike the dashboard, paused
// boxes aren't ordered by last activity: ls has no column showing it, so
// the order would look random next to AGE.
func stateRank(state string) int {
	switch state {
	case "provisioning", "running", "stopping":
		return 0
	case "paused":
		return 1
	default:
		return 2
	}
}

// age renders a rough, human-sized duration ("3h12m", "2d") -- good
// enough for an `ls` column, not meant to be precise to the second.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd%dh", days, hours)
}

func init() {
	addOutputFlag(lsCmd, "wide")
	lsCmd.Flags().BoolP("all", "a", false, "also list paused and stopped boxes")
	lsCmd.Flags().BoolP("project", "p", false, "only this directory's project's boxes: its own, its runs' and its goals'")
	lsCmd.Flags().StringArrayP("label", "l", nil, "only boxes with this label, key or key=value (repeatable, all must match)")
	rootCmd.AddCommand(lsCmd)
}

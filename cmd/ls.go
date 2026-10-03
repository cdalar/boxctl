package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var lsCmd = &cobra.Command{
	Use:     "ls",
	Aliases: []string{"list"},
	Short:   "List your boxes",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		vms, err := newClient().List(cmd.Context())
		if err != nil {
			return err
		}
		if len(vms) == 0 {
			fmt.Println("No boxes yet. Create one with `boxctl create <name>`.")
			return nil
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

		// Every row goes through one tabwriter, so columns line up across
		// sections, and the section labels are slotted in afterwards: a
		// label written through the tabwriter would end the column block
		// and each section would realign on its own.
		var table strings.Builder
		fmt.Fprintln(&table, "NAME\tSTATE\tIP\tIMAGE\tSIZE\tPROVIDER\tREADY\tAGE")
		counts := make([]int, len(lsSections))
		for _, vm := range vms {
			counts[stateRank(vm.State)]++
			ip := vm.IP
			if ip == "" {
				ip = "-"
			}
			image := vm.Image
			if image == "" {
				image = "-"
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

		// Like `onctl ls`: with boxes in one state only, a plain table;
		// otherwise a labelled section per state, each under its own
		// header row.
		groups := 0
		for _, n := range counts {
			if n > 0 {
				groups++
			}
		}
		var out strings.Builder
		if groups < 2 {
			fmt.Fprintln(&out, header)
			for _, row := range rows {
				fmt.Fprintln(&out, row)
			}
			_, err := os.Stdout.WriteString(out.String())
			return err
		}
		color := useColor()
		first := true
		for rank, n := range counts {
			if n == 0 {
				continue
			}
			if !first {
				fmt.Fprintln(&out)
			}
			first = false
			label := fmt.Sprintf("● %s (%d)", lsSections[rank].label, n)
			if color {
				label = lsSections[rank].color + label + "\033[0m"
			}
			fmt.Fprintln(&out, label)
			fmt.Fprintln(&out, header)
			for _, row := range rows[:n] {
				fmt.Fprintln(&out, row)
			}
			rows = rows[n:]
		}
		_, err = os.Stdout.WriteString(out.String())
		return err
	},
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
	rootCmd.AddCommand(lsCmd)
}

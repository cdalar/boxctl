package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tSTATE\tIP\tIMAGE\tSIZE\tPROVIDER\tREADY\tAGE"); err != nil {
			return err
		}
		for _, vm := range vms {
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
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", vm.Name, vm.State, ip, image, sizeLabel(vm), vm.Provider, ready, age(vm.CreatedAt)); err != nil {
				return err
			}
		}
		return w.Flush()
	},
}

// stateRank groups `ls` the same way boxctl.io's dashboard does
// (boxctl-web's STATE_RANK): live boxes -- usable, or on their way there
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

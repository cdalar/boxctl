package cmd

import (
	"fmt"
	"os"
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

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tIP\tPROVIDER\tREADY\tAGE")
		for _, vm := range vms {
			ip := vm.IP
			if ip == "" {
				ip = "-"
			}
			ready := "no"
			if vm.Ready {
				ready = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", vm.Name, vm.State, ip, vm.Provider, ready, age(vm.CreatedAt))
		}
		return w.Flush()
	},
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

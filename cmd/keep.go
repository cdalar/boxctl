package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var keepCmd = &cobra.Command{
	Use:               "keep <name>",
	ValidArgsFunction: completeBoxName,
	Short:             "Hold a box off the idle reaper's deletion for 24 hours",
	Long: `A box the server paused for being unused is deleted after 24 hours
paused. keep holds that off for 24 hours from now; running it again
restarts those 24 hours, it doesn't add to them. Resuming the box stops
the deletion altogether.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		vm, err := newClient().Keep(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if vm.KeepUntil == nil {
			// An older server answers without the field.
			fmt.Printf("%s is kept\n", vm.Name)
			return nil
		}
		fmt.Printf("%s is kept until %s\n", vm.Name, vm.KeepUntil.Local().Format(time.DateTime))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(keepCmd)
}

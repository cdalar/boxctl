package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:   "pause <name>",
	Short: "Pause a box (snapshot and stop; resume later with `boxctl resume`)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Pausing %s (this can take a few minutes for a large box)...\n", name)
		vm, err := newClient().Pause(cmd.Context(), name)
		if err != nil {
			return err
		}
		fmt.Printf("%s is now %s\n", vm.Name, vm.State)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}

package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var pauseCmd = &cobra.Command{
	Use:               "pause <name|all>",
	ValidArgsFunction: completeBoxNameOrAll,
	Short:             "Pause a box (snapshot and stop; resume later with `boxctl resume`)",
	Args:              cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		c := newClient()
		if name == allBoxes {
			fmt.Println("Pausing all running boxes (this can take a few minutes for a large box)...")
			return forEachBox(cmd.Context(), c, "pause", []string{"running"}, func(ctx context.Context, name string) error {
				_, err := c.Pause(ctx, name)
				return err
			})
		}
		sp := startSpinner(fmt.Sprintf("Pausing %s %s", name, dim("(this can take a few minutes for a large box)")))
		vm, err := c.Pause(cmd.Context(), name)
		if err != nil {
			sp.fail(fmt.Sprintf("%s: %v", name, err))
			return err
		}
		sp.finish(fmt.Sprintf("%s is now %s", bold(vm.Name), stateText(vm.State)))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pauseCmd)
}

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
		fmt.Printf("Pausing %s (this can take a few minutes for a large box)...\n", name)
		vm, err := c.Pause(cmd.Context(), name)
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

package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:               "resume <name|all>",
	ValidArgsFunction: completeBoxNameOrAll,
	Short:             "Resume a paused box",
	Args:              cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		c := newClient()
		if name == allBoxes {
			fmt.Println("Resuming all paused boxes (this can take a few minutes for a large box)...")
			return forEachBox(cmd.Context(), c, "resume", []string{"paused"}, func(ctx context.Context, name string) error {
				_, err := c.Resume(ctx, name)
				return err
			})
		}
		sp := startSpinner(fmt.Sprintf("Resuming %s %s", name, dim("(this can take a few minutes for a large box)")))
		vm, err := c.Resume(cmd.Context(), name)
		if err != nil {
			sp.fail(fmt.Sprintf("%s: %v", name, err))
			return err
		}
		sp.finish(fmt.Sprintf("%s is now %s", bold(vm.Name), stateText(vm.State)))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}

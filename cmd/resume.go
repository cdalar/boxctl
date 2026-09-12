package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <name>",
	Short: "Resume a paused box",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Resuming %s (this can take a few minutes for a large box)...\n", name)
		vm, err := newClient().Resume(cmd.Context(), name)
		if err != nil {
			return err
		}
		fmt.Printf("%s is now %s\n", vm.Name, vm.State)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	createTemplate string
	createImage    string
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new box",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Creating %s...\n", name)

		vm, err := newClient().Create(cmd.Context(), name, createTemplate, createImage)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s (%s)\n", vm.Name, vm.State)
		return nil
	},
}

func init() {
	createCmd.Flags().StringVarP(&createTemplate, "template", "t", "", "onctl-templates config path to apply (e.g. k3s/k3s-server.sh)")
	createCmd.Flags().StringVarP(&createImage, "image", "i", "", "boot image to use (defaults to boxctl-vms's own default)")
	rootCmd.AddCommand(createCmd)
}

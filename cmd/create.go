package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// defaultImage is what create and exec boot when --image isn't given.
// boxctl-vms has no default of its own (a create without an image is a
// 400), so the choice lives here -- keep it in step with boxctl-web's
// DEFAULT_IMAGE (lib/images.ts).
const defaultImage = "debian-slim"

var (
	createApplyFile string
	createImage     string
	createSize      string
	createIdleTTL   time.Duration
)

var createCmd = &cobra.Command{
	Use:     "create <name>",
	Aliases: []string{"up"},
	Short:   "Create a new box",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Creating %s...\n", name)

		c := newClient()
		vm, err := c.Create(cmd.Context(), name, createApplyFile, createImage, createSize)
		if err != nil {
			return err
		}
		fmt.Printf("Created %s (%s, %s)\n", vm.Name, vm.State, sizeLabel(*vm))
		if cmd.Flags().Changed("idle-ttl") {
			if _, err := c.SetIdleTTL(cmd.Context(), name, createIdleTTL); err != nil {
				return fmt.Errorf("setting %s's idle TTL: %w", name, err)
			}
			fmt.Printf("%s pauses after %s unused\n", name, createIdleTTL)
		}
		return nil
	},
}

func init() {
	createCmd.Flags().StringVarP(&createApplyFile, "apply-file", "a", "", "onctl-templates script to run on the new box, like onctl up -a (e.g. k3s/k3s-server.sh)")
	// The flag's original name, kept so existing commands still work.
	// Deprecated flags are hidden from --help and print a notice when used.
	createCmd.Flags().StringVarP(&createApplyFile, "template", "t", "", "")
	_ = createCmd.Flags().MarkDeprecated("template", "use --apply-file/-a instead")
	createCmd.Flags().StringVarP(&createImage, "image", "i", defaultImage, "boot image to use (list them with boxctl images)")
	createCmd.Flags().StringVarP(&createSize, "size", "s", "", "box size: small, medium or large (list them with boxctl sizes; default small)")
	_ = createCmd.RegisterFlagCompletionFunc("size", completeSize)
	createCmd.Flags().DurationVar(&createIdleTTL, "idle-ttl", 0, "pause the box after this long unused, 10m to 720h (default: the server's, 6h; 0 never)")
	rootCmd.AddCommand(createCmd)
}

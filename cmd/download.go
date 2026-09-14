package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var downloadOutput string

var downloadCmd = &cobra.Command{
	Use:   "download <name>",
	Short: "Download a paused box's snapshot bundle",
	Long: `Download a paused box's snapshot bundle -- rootfs, memory/device
snapshot, kernel, and manifest, as one .tar.zst file. The box must
already be paused (see 'boxctl pause').

This also leaves a durable backup behind in the boxctl-web dashboard's
Backups tab (aging out after the normal retention period) -- it's the
same backup the dashboard's own Backup button creates.

Re-create the box elsewhere with 'boxctl import'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		outPath := downloadOutput
		if outPath == "" {
			outPath = name + ".tar.zst"
		}

		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("creating %s: %w", outPath, err)
		}
		defer func() { _ = f.Close() }()

		fmt.Printf("Downloading %s -> %s...\n", name, outPath)
		if err := newClient().Download(cmd.Context(), name, f); err != nil {
			// Don't leave a truncated/empty file behind on failure -- a
			// partial .tar.zst would otherwise look like a real bundle to
			// a later `boxctl import`.
			_ = f.Close()
			_ = os.Remove(outPath)
			return err
		}
		fmt.Printf("Saved %s\n", outPath)
		return nil
	},
}

func init() {
	downloadCmd.Flags().StringVarP(&downloadOutput, "output", "o", "", "output file path (default: <name>.tar.zst)")
	rootCmd.AddCommand(downloadCmd)
}

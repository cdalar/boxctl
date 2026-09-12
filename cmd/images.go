package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var imagesCmd = &cobra.Command{
	Use:     "images",
	Aliases: []string{"image"},
	Short:   "List available boot images",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		images, err := newClient().ListImages(cmd.Context())
		if err != nil {
			return err
		}
		if len(images) == 0 {
			fmt.Println("No images available.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tPROVIDER\tDESCRIPTION"); err != nil {
			return err
		}
		for _, img := range images {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", img.Name, img.Provider, img.Description); err != nil {
				return err
			}
		}
		return w.Flush()
	},
}

func init() {
	rootCmd.AddCommand(imagesCmd)
}

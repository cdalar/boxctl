package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var imagesCmd = &cobra.Command{
	Use:     "images",
	Aliases: []string{"image"},
	Short:   "List available boot images",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := wantJSON(cmd)
		if err != nil {
			return err
		}
		images, err := newClient().ListImages(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(images)
		}
		if len(images) == 0 {
			fmt.Println("No images available.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tPROVIDER\tDESCRIPTION"); err != nil {
			return err
		}
		// On a terminal, cut each description to what's left of the line after
		// the NAME and PROVIDER columns, so a long one doesn't wrap onto a
		// second line. Piped output keeps the full text.
		room := 0
		if cols, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			nameW, provW := len("NAME"), len("PROVIDER")
			for _, img := range images {
				nameW = max(nameW, utf8.RuneCountInString(img.Name))
				provW = max(provW, utf8.RuneCountInString(img.Provider))
			}
			room = max(cols-(nameW+2+provW+2), 10)
		}
		for _, img := range images {
			desc := img.Description
			if room > 0 {
				desc = truncateLine(desc, room)
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", img.Name, img.Provider, desc); err != nil {
				return err
			}
		}
		return w.Flush()
	},
}

func init() {
	addOutputFlag(imagesCmd)
	rootCmd.AddCommand(imagesCmd)
}

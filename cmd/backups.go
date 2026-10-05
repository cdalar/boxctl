package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

var backupsCmd = &cobra.Command{
	Use:     "backups",
	Aliases: []string{"backup"},
	Short:   "List your backups",
	Long: `List your backups, newest first -- the saved copies of paused boxes
that 'boxctl download' and the dashboard's Backup button create.

Bring one back as a new box with 'boxctl restore'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := wantJSON(cmd)
		if err != nil {
			return err
		}
		backups, err := listBackups(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(backups)
		}
		if len(backups) == 0 {
			fmt.Println("No backups yet. Pause a box and run `boxctl download <name>` to make one.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "ID\tBOX\tSIZE\tSTATUS\tAGE"); err != nil {
			return err
		}
		for _, b := range backups {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", b.ID, b.VMName, client.FormatBytes(b.SizeBytes), b.UploadStatus, age(b.CreatedAt)); err != nil {
				return err
			}
		}
		return w.Flush()
	},
}

// listBackups is the caller's backups, newest first.
func listBackups(ctx context.Context) ([]client.Backup, error) {
	backups, err := newClient().ListBackups(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(backups, func(i, j int) bool { return backups[i].CreatedAt.After(backups[j].CreatedAt) })
	return backups, nil
}

func init() {
	addOutputFlag(backupsCmd)
	rootCmd.AddCommand(backupsCmd)
}

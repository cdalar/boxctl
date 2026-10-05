package cmd

import (
	"context"
	"fmt"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

var (
	restoreName string
	restoreSize string
)

var restoreCmd = &cobra.Command{
	Use:               "restore <backup>",
	ValidArgsFunction: completeBackup,
	Short:             "Restore a backup as a new box",
	Long: `Restore a backup as a new box. <backup> is a backup's id (see
'boxctl backups'), or the name of the box it was taken from, meaning
that box's newest backup.

Without --size the box comes back paused, exactly as it was backed up --
run 'boxctl resume' to carry on where it left off.

With a --size other than the backup's own, the box is started fresh from
the backup's disk at that size: every file is kept, but programs that
were running when the backup was taken aren't. The disk grows to the new
size's and never shrinks.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		backups, err := listBackups(cmd.Context())
		if err != nil {
			return err
		}
		b, err := findBackup(backups, args[0])
		if err != nil {
			return err
		}
		name := restoreName
		if name == "" {
			name = b.VMName
		}

		fmt.Printf("Restoring %s's backup from %s ago as %s...\n", b.VMName, age(b.CreatedAt), name)
		vm, err := newClient().Restore(cmd.Context(), b.ID, name, restoreSize)
		if err != nil {
			return err
		}
		if vm.State == "paused" {
			fmt.Printf("Restored %s (%s, %s) -- run `boxctl resume %s` to boot it\n", vm.Name, vm.State, sizeLabel(*vm), vm.Name)
			return nil
		}
		fmt.Printf("Restored %s (%s, %s)\n", vm.Name, vm.State, sizeLabel(*vm))
		return nil
	},
}

// findBackup resolves restore's argument against backups (newest first):
// a backup id, else the newest backup of the box with that name.
func findBackup(backups []client.Backup, arg string) (client.Backup, error) {
	for _, b := range backups {
		if b.ID == arg {
			return b, nil
		}
	}
	for _, b := range backups {
		if b.VMName == arg {
			return b, nil
		}
	}
	return client.Backup{}, fmt.Errorf("no backup with id %q, and no backup of a box named %q -- list them with `boxctl backups`", arg, arg)
}

// completeBackup offers the names of boxes that have a backup, then every
// backup id, failing silently to no suggestions like completeBoxName.
func completeBackup(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 || cfg == nil || cfg.Token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	backups, err := listBackups(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	var out []cobra.Completion
	seen := map[string]bool{}
	for _, b := range backups {
		if !seen[b.VMName] {
			seen[b.VMName] = true
			out = append(out, cobra.CompletionWithDesc(b.VMName, "newest backup, "+age(b.CreatedAt)+" ago"))
		}
	}
	for _, b := range backups {
		out = append(out, cobra.CompletionWithDesc(b.ID, b.VMName+", "+age(b.CreatedAt)+" ago"))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	restoreCmd.Flags().StringVarP(&restoreName, "name", "n", "", "name for the restored box (default: the name of the box the backup was taken from)")
	restoreCmd.Flags().StringVarP(&restoreSize, "size", "s", "", "restore at this size instead of the backup's own (list them with boxctl sizes)")
	_ = restoreCmd.RegisterFlagCompletionFunc("size", completeSize)
	rootCmd.AddCommand(restoreCmd)
}

package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

var sizesCmd = &cobra.Command{
	Use:     "sizes",
	Aliases: []string{"size"},
	Short:   "List available box sizes",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := wantJSON(cmd)
		if err != nil {
			return err
		}
		sizes, err := newClient().ListSizes(cmd.Context())
		if err != nil {
			return err
		}
		if asJSON {
			return printJSON(sizes)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tVCPU\tMEMORY\tDISK"); err != nil {
			return err
		}
		for _, s := range sizes {
			name := s.Name
			if s.Default {
				name += " (default)"
			}
			// "-" only from a server that predates per-size disks.
			disk := "-"
			if s.DiskMiB > 0 {
				disk = memLabel(s.DiskMiB)
			}
			if _, err := fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", name, s.VCPU, memLabel(s.MemMiB), disk); err != nil {
				return err
			}
		}
		return w.Flush()
	},
}

func init() {
	addOutputFlag(sizesCmd)
	rootCmd.AddCommand(sizesCmd)
}

// sizeLabel renders a box's size for `ls`/`create`: the preset name when
// it has one, else its raw shape, else "-" (a server that predates
// sizes reports neither).
func sizeLabel(vm client.VM) string {
	switch {
	case vm.Size != "":
		return vm.Size
	case vm.VCPU > 0:
		return fmt.Sprintf("%dvcpu/%s", vm.VCPU, memLabel(vm.MemMiB))
	default:
		return "-"
	}
}

func memLabel(mib int) string {
	if mib%1024 == 0 {
		return fmt.Sprintf("%dGiB", mib/1024)
	}
	return fmt.Sprintf("%dMiB", mib)
}

// completeSize offers the server's size names for --size, failing
// silently to no suggestions like completeBoxName.
func completeSize(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if cfg == nil || cfg.Token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	sizes, err := newClient().ListSizes(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]cobra.Completion, 0, len(sizes))
	for _, s := range sizes {
		out = append(out, cobra.CompletionWithDesc(s.Name, fmt.Sprintf("%d vCPU, %s", s.VCPU, memLabel(s.MemMiB))))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

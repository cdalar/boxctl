package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

var rmForce bool

var rmCmd = &cobra.Command{
	Use:               "rm <name...|all>",
	ValidArgsFunction: completeBoxNamesOrAll,
	Aliases:           []string{"destroy", "delete"},
	Short:             "Destroy one or more boxes",
	Args:              cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		if slices.Contains(args, allBoxes) {
			if len(args) > 1 {
				return fmt.Errorf("%q can't be combined with box names", allBoxes)
			}
			if !rmForce && !confirm("Destroy ALL your boxes? This cannot be undone.") {
				fmt.Println("Aborted.")
				return nil
			}
			if !confirmUnfinished(cmd.Context(), c, nil) {
				fmt.Println("Aborted.")
				return nil
			}
			return forEachBox(cmd.Context(), c, "destroy", nil, c.Destroy)
		}

		// Drop repeated names: the second destroy would only fail.
		var names []string
		for _, name := range args {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}

		if !rmForce && !confirm(fmt.Sprintf("Destroy %s? This cannot be undone.", strings.Join(names, ", "))) {
			fmt.Println("Aborted.")
			return nil
		}
		if !confirmUnfinished(cmd.Context(), c, names) {
			fmt.Println("Aborted.")
			return nil
		}

		// One by one, in the order given; a failure doesn't stop the rest.
		failed := 0
		for _, name := range names {
			sp := startSpinner("Destroying " + name)
			if err := c.Destroy(cmd.Context(), name); err != nil {
				sp.fail(fmt.Sprintf("%s: %v", name, err))
				failed++
				continue
			}
			if len(names) == 1 {
				sp.finish("Destroyed " + bold(name))
				continue
			}
			sp.finish(name)
		}
		if failed > 0 {
			if len(names) == 1 {
				return fmt.Errorf("failed to destroy %s", names[0])
			}
			return fmt.Errorf("failed to destroy %d of %d box(es)", failed, len(names))
		}
		return nil
	},
}

// unfinishedRuns is the boxes among names (every box, if names is empty)
// that are a --goal run whose run_status isn't "done": still running,
// failed, or not reporting at all ("?") -- destroying one loses work
// that hasn't been fetched. Boxes that aren't goal runs have no status
// to check. A failed listing is a warning, never a reason not to destroy.
func unfinishedRuns(ctx context.Context, c *client.Client, names []string) []client.VM {
	vms, err := c.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't check whether the runs are done: %v\n", err)
		return nil
	}
	return filterUnfinished(vms, names)
}

// filterUnfinished is unfinishedRuns' selection, apart from the listing.
func filterUnfinished(vms []client.VM, names []string) []client.VM {
	var out []client.VM
	for _, vm := range vms {
		if len(names) > 0 && !slices.Contains(names, vm.Name) {
			continue
		}
		if vm.Labels["boxctl.run"] != "goal" {
			continue
		}
		if st, err := parseGoalStatus(vm.RunStatus); err == nil && st != nil && st.Status == "done" {
			continue
		}
		out = append(out, vm)
	}
	return out
}

// confirmUnfinished is rm's second confirmation, asked after the usual
// one and only when a goal run among names isn't done: one line per box
// with its RUN text ("claude goal: running"), then a separate prompt.
// Done runs (and boxes that aren't runs) go straight through, and
// --force skips it like the first.
func confirmUnfinished(ctx context.Context, c *client.Client, names []string) bool {
	if rmForce {
		return true
	}
	vms := unfinishedRuns(ctx, c, names)
	if len(vms) == 0 {
		return true
	}
	for _, vm := range vms {
		fmt.Printf("%s %s is not done (%s)\n", warnMark(), vm.Name, runText(vm))
	}
	return confirm("Its work may not be fetched yet. Destroy it anyway?")
}

// confirm prompts on stdin, defaulting to "no" on anything but an
// explicit y/yes -- an unattended script should pass --force instead of
// relying on stdin behavior.
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "skip the confirmation prompt")
	rootCmd.AddCommand(rmCmd)
}

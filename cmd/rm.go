package cmd

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"

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

		// One by one, in the order given; a failure doesn't stop the rest.
		failed := 0
		for _, name := range names {
			if err := c.Destroy(cmd.Context(), name); err != nil {
				fmt.Printf("\033[31m✘\033[0m %s: %v\n", name, err)
				failed++
				continue
			}
			if len(names) == 1 {
				fmt.Printf("Destroyed %s\n", name)
				continue
			}
			fmt.Printf("\033[32m✔\033[0m %s\n", name)
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

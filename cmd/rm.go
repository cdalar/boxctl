package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var rmForce bool

var rmCmd = &cobra.Command{
	Use:     "rm <name>",
	Aliases: []string{"destroy", "delete"},
	Short:   "Destroy a box",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !rmForce && !confirm(fmt.Sprintf("Destroy %s? This cannot be undone.", name)) {
			fmt.Println("Aborted.")
			return nil
		}

		if err := newClient().Destroy(cmd.Context(), name); err != nil {
			return err
		}
		fmt.Printf("Destroyed %s\n", name)
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

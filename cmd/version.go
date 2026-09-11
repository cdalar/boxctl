package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is overridden at release build time via
// -ldflags "-X boxctl-cli/cmd.version=..." (see onctl's own .goreleaser.yml
// for the equivalent pattern this mirrors).
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the boxctl version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("boxctl", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version/BuildTime/GoVersion are overridden at build time via
// -ldflags "-X boxctl-cli/cmd.Version=..." -- see the Makefile, which
// mirrors onctl's own.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GoVersion = "unknown"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the boxctl version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("boxctl %s (built %s, %s)\n", Version, BuildTime, GoVersion)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

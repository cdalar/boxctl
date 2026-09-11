package cmd

import (
	"fmt"

	"boxctl-cli/internal/config"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Forget the saved personal API token on this machine",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := config.Clear(); err != nil {
			return fmt.Errorf("removing config: %w", err)
		}
		// This only removes the local copy -- the token itself stays
		// valid server-side until revoked from the dashboard.
		fmt.Printf("Logged out. To revoke the token itself, visit %s\n", dashboardURL)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

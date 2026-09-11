package cmd

import (
	"fmt"

	"boxctl-cli/internal/client"
	"boxctl-cli/internal/config"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login <token>",
	Short: "Save a personal API token (create one at " + dashboardURL + ")",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL := config.DefaultAPIURL
		if apiURLFlag != "" {
			apiURL = apiURLFlag
		}

		token := args[0]
		// Fail fast on a bad token/URL now, rather than saving it and
		// having every later command fail with a confusing 401.
		if _, err := client.New(apiURL, token).List(cmd.Context()); err != nil {
			return fmt.Errorf("that token didn't work against %s: %w", apiURL, err)
		}

		if err := config.Save(&config.Config{APIURL: apiURL, Token: token}); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Println("Logged in.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCmd)
}

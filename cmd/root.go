// Package cmd implements boxctl's subcommands (Cobra), mirroring the
// onctl CLI's structure (github.com/cdalar/onctl/cmd) but talking to the
// hosted boxctl-vms control plane over HTTPS instead of driving a local
// cloud provider SDK directly.
package cmd

import (
	"fmt"

	"boxctl-cli/internal/client"
	"boxctl-cli/internal/config"

	"github.com/spf13/cobra"
)

// dashboardURL is where a user creates a personal token (see
// boxctl-web's app/dashboard/tokens/). Referenced only in help/error
// text, never dialed by this binary.
const dashboardURL = "https://boxctl.io/dashboard/tokens"

// commandsWithoutLogin don't need an existing token: login itself, plus
// the handful of commands that are meaningful (or must at least run)
// with no session at all.
var commandsWithoutLogin = map[string]bool{
	"login":      true,
	"logout":     true,
	"version":    true,
	"help":       true,
	"completion": true,
}

var (
	apiURLFlag string
	cfg        *config.Config
)

var rootCmd = &cobra.Command{
	Use:   "boxctl",
	Short: "Manage your boxctl.io Firecracker microVMs from the command line",
	Long: `boxctl talks to the boxctl-vms control plane behind boxctl.io to boot,
list, connect to, and destroy your Firecracker microVMs ("boxes").`,
	Example: `  # Save your personal access token (create one at ` + dashboardURL + `)
  boxctl login <token>

  # List your boxes
  boxctl ls

  # Create a box
  boxctl create my-box

  # Open a terminal on a box
  boxctl ssh my-box

  # Destroy a box
  boxctl rm my-box`,
	SilenceUsage: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		loaded, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg = loaded
		if apiURLFlag != "" {
			cfg.APIURL = apiURLFlag
		}

		if commandsWithoutLogin[cmd.Name()] {
			return nil
		}
		if cfg.Token == "" {
			return fmt.Errorf("not logged in -- run `boxctl login <token>` first (create one at %s)", dashboardURL)
		}
		return nil
	},
}

// Execute runs the root command; main.go's only job is to call this and
// report a non-nil error.
func Execute() error {
	return rootCmd.Execute()
}

// newClient builds an API client from the config PersistentPreRunE
// already loaded -- every leaf command's RunE calls this instead of
// touching cfg directly.
func newClient() *client.Client {
	return client.New(cfg.APIURL, cfg.Token)
}

func init() {
	rootCmd.PersistentFlags().StringVar(&apiURLFlag, "api-url", "", "override the boxctl API URL (default: "+config.DefaultAPIURL+", or whatever was saved by a previous login)")
}

// Package cmd implements boxctl's subcommands (Cobra), mirroring the
// onctl CLI's structure (github.com/cdalar/onctl/cmd) but talking to the
// hosted boxctl-vms control plane over HTTPS instead of driving a local
// cloud provider SDK directly.
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"

	"github.com/spf13/cobra"
)

// dashboardURL is where a user creates a personal token (see
// boxctl-web's app/dashboard/tokens/). Referenced only in help/error
// text, never dialed by this binary.
const dashboardURL = "https://boxctl.io/dashboard/tokens"

// tokenEnv is the environment variable that supplies the personal token
// without a `boxctl login`: every command uses it over the saved config
// when it's set, and `login` saves it so later shells need it no more.
const tokenEnv = "BOXCTL_TOKEN"

// commandsWithoutLogin don't need an existing token: login itself, plus
// the handful of commands that are meaningful (or must at least run)
// with no session at all.
var commandsWithoutLogin = map[string]bool{
	"login":      true,
	"logout":     true,
	"version":    true,
	"help":       true,
	"completion": true,
	// cobra's hidden tab-completion commands: a logged-out user should
	// just get no suggestions, not an error (see completeBoxName).
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
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
  boxctl login

  # List your running boxes (-a for paused ones too)
  boxctl ls

  # List available boot images
  boxctl images

  # Create a box
  boxctl create my-box

  # Open a terminal on a box
  boxctl ssh my-box

  # Destroy a box
  boxctl rm my-box`,
	SilenceUsage:  true,
	SilenceErrors: true, // main.go prints the error once; don't let cobra print it too
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		loaded, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg = loaded
		if apiURLFlag != "" {
			cfg.APIURL = apiURLFlag
		}
		// BOXCTL_TOKEN beats the saved token, like --api-url beats the
		// saved URL: with it set, no `boxctl login` is needed at all
		// (scripts, CI, a box with the token in its environment).
		if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
			cfg.Token = token
		}

		// Check the whole command path, not just the leaf: `completion
		// bash` runs a command named "bash", and it must work before
		// login (get.sh runs it on a freshly downloaded binary).
		for c := cmd; c != nil; c = c.Parent() {
			if commandsWithoutLogin[c.Name()] {
				return nil
			}
		}
		if cfg.Token == "" {
			return fmt.Errorf("not logged in -- run `boxctl login` first or set %s (create one at %s)", tokenEnv, dashboardURL)
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

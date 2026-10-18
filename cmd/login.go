package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Save a personal API token (create one at " + dashboardURL + ")",
	Long: `Save a personal API token to ~/.boxctl/config.json.

The token comes from the ` + tokenEnv + ` environment variable when that is
set; otherwise it is read from a hidden prompt when run interactively, or
from stdin when piped.

With ` + tokenEnv + ` set, every command already uses it and login is only
needed to save it for shells that won't have the variable.`,
	Example: `  # Interactive: paste the token at the hidden prompt
  boxctl login

  # Non-interactive: from the environment
  BOXCTL_TOKEN=... boxctl login

  # Non-interactive: pipe it in
  pbpaste | boxctl login
  boxctl login < token.txt`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL := config.DefaultAPIURL
		if apiURLFlag != "" {
			apiURL = apiURLFlag
		}

		token, err := readToken(os.Getenv(tokenEnv), os.Stdin)
		if err != nil {
			return err
		}

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

// readToken returns the token: fromEnv (the BOXCTL_TOKEN value) when it
// isn't blank, else a no-echo prompt when in is a terminal, else the
// first line of in (so `pbpaste | boxctl login` and `boxctl login < file`
// both work). The prompt goes to stderr so it never mixes with piped
// output.
func readToken(fromEnv string, in *os.File) (string, error) {
	if token := strings.TrimSpace(fromEnv); token != "" {
		return token, nil
	}

	var raw string
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(os.Stderr, "Paste your personal token (input hidden; create one at %s): ", dashboardURL)
		b, err := term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("reading token: %w", err)
		}
		raw = string(b)
	} else {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading token from stdin: %w", err)
		}
		raw = line
	}

	token := strings.TrimSpace(raw)
	if token == "" {
		return "", errors.New("no token given; set " + tokenEnv + ", paste it at the prompt, or pipe it in (`pbpaste | boxctl login`)")
	}
	return token, nil
}

func init() {
	rootCmd.AddCommand(loginCmd)
}

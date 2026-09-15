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

The token is deliberately not accepted as a command-line argument, since
that would leave it in your shell history. Instead, login reads it from
a hidden prompt when run interactively, or from stdin when piped.`,
	Example: `  # Interactive: paste the token at the hidden prompt
  boxctl login

  # Non-interactive: pipe it in (never touches your shell history)
  pbpaste | boxctl login
  boxctl login < token.txt
  printf '%s' "$BOXCTL_TOKEN" | boxctl login`,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return errors.New("login no longer takes the token as an argument (it would end up in your shell history); " +
				"run `boxctl login` and paste it at the prompt, or pipe it in: `pbpaste | boxctl login`")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL := config.DefaultAPIURL
		if apiURLFlag != "" {
			apiURL = apiURLFlag
		}

		token, err := readToken(os.Stdin)
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

// readToken gets the token without it ever being a process argument:
// from a no-echo prompt when stdin is a terminal, otherwise the first
// line of stdin (so `pbpaste | boxctl login` and `boxctl login < file`
// both work). The prompt goes to stderr so it never mixes with piped
// output.
func readToken(in *os.File) (string, error) {
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
		return "", errors.New("no token given; paste it at the prompt or pipe it in (`pbpaste | boxctl login`)")
	}
	return token, nil
}

func init() {
	rootCmd.AddCommand(loginCmd)
}

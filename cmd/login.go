package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	loginNoBrowser bool
	loginPaste     bool
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in through your browser, or save a personal API token",
	Long: `Log in to boxctl.io and save a personal API token to ~/.boxctl/config.json.

On a terminal, login opens your browser: sign in, click Approve, and this
computer is logged in. Over ssh, or with --no-browser, it prints the URL
to open on any computer instead and waits for the approval.

A token made at ` + dashboardURL + ` works too: it comes
from the ` + tokenEnv + ` environment variable when that is set, from stdin
when piped, or from a hidden prompt with --paste. With ` + tokenEnv + ` set,
every command already uses it and login is only needed to save it for
shells that won't have the variable.`,
	Example: `  # Browser: sign in and click Approve
  boxctl login

  # The browser is on another computer: open the printed URL there
  boxctl login --no-browser

  # A token from the dashboard instead
  boxctl login --paste
  BOXCTL_TOKEN=... boxctl login
  pbpaste | boxctl login
  boxctl login < token.txt`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		apiURL := config.DefaultAPIURL
		if apiURLFlag != "" {
			apiURL = apiURLFlag
		}

		token, email, err := obtainToken(cmd, apiURL)
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
		if email != "" {
			fmt.Printf("%s Logged in as %s.\n", okMark(), email)
		} else {
			fmt.Printf("%s Logged in.\n", okMark())
		}
		return nil
	},
}

// obtainToken is where the token comes from: the environment, piped
// stdin, or the --paste prompt give a token the user already has;
// otherwise the browser login mints one (and may say whose it is).
// A server without browser login falls back to the prompt.
func obtainToken(cmd *cobra.Command, apiURL string) (token, email string, err error) {
	pasted := loginPaste || strings.TrimSpace(os.Getenv(tokenEnv)) != "" || !term.IsTerminal(int(os.Stdin.Fd()))
	if !pasted {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		st, err := newBrowserLogin(client.New(apiURL, ""), loginNoBrowser).run(ctx)
		if err == nil {
			return st.Token, st.Email, nil
		}
		if !errors.Is(err, client.ErrNoBrowserLogin) {
			return "", "", err
		}
		sayNote("%s has no browser login; paste a token instead.", apiURL)
	}
	token, err = readToken(os.Getenv(tokenEnv), os.Stdin)
	return token, "", err
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
	loginCmd.Flags().BoolVar(&loginNoBrowser, "no-browser", false, "don't open a browser or listen for it: print the URL to open on any computer")
	loginCmd.Flags().BoolVar(&loginPaste, "paste", false, "paste a token from "+dashboardURL+" at a hidden prompt instead of logging in through the browser")
	rootCmd.AddCommand(loginCmd)
}

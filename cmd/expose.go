package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/boxctl-io/boxctl-cli/internal/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	exposeHostHeader string
	exposeNoWait     bool

	exposeAuthUser      string
	exposeNoAuth        bool
	exposePasswordStdin bool
)

// exposePasswordEnv holds the basic-auth password for `expose`, for
// scripts: a flag value would land in shell history and `ps`.
const exposePasswordEnv = "BOXCTL_EXPOSE_PASSWORD"

// exposeWait is how long `expose` waits for a new URL to start serving
// before printing it anyway. The server normally registers it within a
// few seconds.
const exposeWait = 30 * time.Second

var exposeCmd = &cobra.Command{
	Use:               "expose <name> [port]",
	ValidArgsFunction: completeBoxName,
	Short:             "Give a port inside a box a public HTTPS URL",
	Long: `Puts a port of a box on the internet at its own HTTPS URL, for sharing
a dev server, demo or webhook endpoint with someone who isn't you.
Anyone with the URL can reach it -- for something only you should see,
use port-forward instead.

  boxctl expose my-box 3000        # https://my-box-k7f2q9xd.boxctl.app -> my-box:3000
  boxctl expose my-box             # list my-box's public URLs
  boxctl expose ls                 # list them across all your boxes
  boxctl unexpose my-box 3000      # take it down

An account can have up to five public URLs, across all its boxes.

The URL is chosen for you and stays the same for as long as the port is
exposed, across pause and resume. While the box is paused the URL shows
a "not available" page.

Two things the app inside the box has to do:

  - listen on 0.0.0.0, not 127.0.0.1 (the box's own localhost isn't
    reachable from outside it)
  - accept the public hostname. Dev servers that only answer to names
    they know (Vite, Rails, Django) will refuse it; either allow the
    hostname in the app, or pass --host-header localhost:3000 to have
    requests arrive under that name instead.

To put a username and password in front of the URL (the password comes from
--basic-auth-password-stdin if given, else BOXCTL_EXPOSE_PASSWORD, else a
hidden prompt):

  boxctl expose my-box 3000 --basic-auth-user demo      # asks for the password (hidden)
  echo "$PW" | boxctl expose my-box 3000 --basic-auth-user demo --basic-auth-password-stdin
  BOXCTL_EXPOSE_PASSWORD=... boxctl expose my-box 3000 --basic-auth-user demo
  boxctl expose my-box 3000 --no-basic-auth             # remove it again

On an already-exposed port the same command changes the credentials; the
URL stays. Without either option the credentials are left as they are.
The password is never accepted as a flag value, and the server never
returns it. The username is 1-64 characters from A-Z a-z 0-9 . _ @ + -;
the password 8-128 printable ASCII characters, no spaces, no { or }.

HTTP, WebSocket and server-sent events work; other TCP protocols don't.
Visitors to the URL count as using the box, so it isn't auto-paused
while people are on it.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		asJSON, err := wantJSON(cmd)
		if err != nil {
			return err
		}
		c := newClient()
		if len(args) == 1 {
			if exposeHostHeader != "" {
				return errors.New("--host-header needs a port to expose")
			}
			if exposeAuthUser != "" || exposeNoAuth || exposePasswordStdin {
				return errors.New("the basic-auth options need a port to expose")
			}
			list, err := c.ListIngress(cmd.Context(), name)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(list)
			}
			if len(list) == 0 {
				fmt.Printf("%s has no public URLs. Expose a port with `boxctl expose %s <port>`.\n", name, name)
				return nil
			}
			return printIngressTable(list)
		}

		port, err := parseExposePort(args[1])
		if err != nil {
			return err
		}
		opts, err := exposeOptions(cmd.InOrStdin(), os.Stdin)
		if err != nil {
			return err
		}
		in, err := c.Expose(cmd.Context(), name, port, opts)
		if err != nil {
			return err
		}
		if in.Status == "pending" && !exposeNoWait {
			in = waitForIngress(cmd.Context(), c, name, in)
		}
		if asJSON {
			return printJSON([]client.Ingress{*in})
		}
		// The URL line goes to stdout on its own, so `$(boxctl expose ...)`
		// is easy to trim; everything explanatory goes to stderr.
		fmt.Printf("%s -> %s:%d\n", in.URL, name, in.Port)
		fmt.Fprintln(os.Stderr, exposeNote(name, in))
		if in.BasicAuth {
			fmt.Fprintln(os.Stderr, curlHint(in))
		}
		return nil
	},
}

var exposeLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List the public URLs of all your boxes",
	Long: `Lists every public URL across all your boxes, paused ones included
(their URLs come back when they resume). To see one box's, run
boxctl expose <name>.`,
	Example: `  boxctl expose ls
  boxctl expose ls -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		asJSON, err := wantJSON(cmd)
		if err != nil {
			return err
		}
		vms, err := newClient().List(cmd.Context())
		if err != nil {
			return err
		}
		var rows []boxIngress
		for _, vm := range vms {
			for _, in := range vm.Ingress {
				rows = append(rows, boxIngress{Box: vm.Name, Ingress: in})
			}
		}
		if asJSON {
			return printJSON(rows)
		}
		if len(rows) == 0 {
			fmt.Println("No public URLs. Expose a port with `boxctl expose <name> <port>`.")
			return nil
		}
		return printBoxIngressTable(rows)
	},
}

// boxIngress is one public URL with the box it belongs to, the shape
// `expose ls` prints and, with -o json, emits.
type boxIngress struct {
	Box string `json:"box"`
	client.Ingress
}

func printBoxIngressTable(rows []boxIngress) error {
	return writeBoxIngressTable(os.Stdout, rows)
}

// writeBoxIngressTable prints the table. The AUTH column comes after AGE
// and only when some URL is protected, so a listing without protected
// URLs is what it was before basic auth existed.
func writeBoxIngressTable(out io.Writer, rows []boxIngress) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	list := make([]client.Ingress, len(rows))
	for i, r := range rows {
		list[i] = r.Ingress
	}
	showAuth := anyProtected(list)
	head := "BOX\tPORT\tSTATUS\tURL\tAGE"
	if showAuth {
		head += "\tAUTH"
	}
	if _, err := fmt.Fprintln(w, head); err != nil {
		return err
	}
	for _, r := range rows {
		line := fmt.Sprintf("%s\t%d\t%s\t%s\t%s", r.Box, r.Port, r.Status, r.URL, age(r.CreatedAt))
		if showAuth {
			line += "\t" + authText(r.Ingress)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return w.Flush()
}

func anyProtected(list []client.Ingress) bool {
	for _, in := range list {
		if in.BasicAuth {
			return true
		}
	}
	return false
}

var unexposeCmd = &cobra.Command{
	Use:               "unexpose <name> <port|all>",
	ValidArgsFunction: completeBoxName,
	Short:             "Take a box's public URL down",
	Long: `Removes the public URL from a port of a box. The URL is retired, not
kept: exposing the port again gives it a new one.

  boxctl unexpose my-box 3000
  boxctl unexpose my-box all`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		c := newClient()
		if args[1] == allBoxes {
			list, err := c.ListIngress(cmd.Context(), name)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Printf("%s has no public URLs.\n", name)
				return nil
			}
			var failed error
			for _, in := range list {
				if err := c.Unexpose(cmd.Context(), name, in.Port); err != nil {
					fmt.Fprintf(os.Stderr, "✘ %s:%d: %v\n", name, in.Port, err)
					failed = errors.Join(failed, err)
					continue
				}
				fmt.Printf("%s:%d is no longer public (was %s)\n", name, in.Port, in.URL)
			}
			return failed
		}
		port, err := parseExposePort(args[1])
		if err != nil {
			return err
		}
		if err := c.Unexpose(cmd.Context(), name, port); err != nil {
			return err
		}
		fmt.Printf("%s:%d is no longer public\n", name, port)
		return nil
	},
}

// parseExposePort checks a port argument locally, so a typo gets a
// plain message rather than the server's.
func parseExposePort(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q -- expected a number from 1 to 65535", s)
	}
	return port, nil
}

// waitForIngress polls until in's URL is serving, the box turns out not
// to be running, or exposeWait runs out, and returns the latest state it
// saw. Never fails: the port is exposed either way, and the caller
// reports whatever status this ends on.
func waitForIngress(ctx context.Context, c *client.Client, name string, in *client.Ingress) *client.Ingress {
	ctx, cancel := context.WithTimeout(ctx, exposeWait)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return in
		case <-ticker.C:
		}
		list, err := c.ListIngress(ctx, name)
		if err != nil {
			return in
		}
		for i := range list {
			if list[i].Port == in.Port {
				in = &list[i]
			}
		}
		if in.Status != "pending" {
			return in
		}
	}
}

// exposeNote is the line under the URL: what state it's in and the one
// thing most likely to be wrong if it doesn't load.
func exposeNote(name string, in *client.Ingress) string {
	switch in.Status {
	case "active":
		return fmt.Sprintf("Live. If it doesn't load, check the app listens on 0.0.0.0:%d inside the box.", in.Port)
	case "paused":
		return fmt.Sprintf("%s isn't running, so the URL shows a \"not available\" page until you `boxctl resume %s`.", name, name)
	case "suspended":
		return "Offline: public URLs aren't included in this account's plan. The URL is kept and serves again once they are."
	default:
		return fmt.Sprintf("Not serving yet -- it should be within a minute. Check with `boxctl expose %s`.", name)
	}
}

func printIngressTable(list []client.Ingress) error {
	return writeIngressTable(os.Stdout, list)
}

// writeIngressTable prints one box's table; AUTH is last and only shown
// when a URL is protected (see writeBoxIngressTable).
func writeIngressTable(out io.Writer, list []client.Ingress) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	showAuth := anyProtected(list)
	head := "PORT\tSTATUS\tURL\tHOST HEADER\tAGE"
	if showAuth {
		head += "\tAUTH"
	}
	if _, err := fmt.Fprintln(w, head); err != nil {
		return err
	}
	for _, in := range list {
		hostHeader := in.HostHeader
		if hostHeader == "" {
			hostHeader = "-"
		}
		line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s", in.Port, in.Status, in.URL, hostHeader, age(in.CreatedAt))
		if showAuth {
			line += "\t" + authText(in)
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return w.Flush()
}

// authText is the AUTH column: the username of a protected URL, "-" for
// an open one.
func authText(in client.Ingress) string {
	if !in.BasicAuth {
		return "-"
	}
	if in.BasicAuthUser == "" {
		return "yes"
	}
	return in.BasicAuthUser
}

// curlHint is a ready-to-run curl line for a protected URL. It has no
// password: curl asks for it.
func curlHint(in *client.Ingress) string {
	user := in.BasicAuthUser
	if user == "" {
		return "Protected by basic auth. Try it with: curl -u <user> " + in.URL
	}
	return fmt.Sprintf("Protected by basic auth. Try it with: curl -u %s %s  (curl asks for the password)", user, in.URL)
}

// exposeOptions turns the flags into the request's options, reading the
// password -- --basic-auth-password-stdin, then the environment, then a hidden
// prompt -- when credentials are being set. stdin is what a terminal
// check and the prompt use.
func exposeOptions(r io.Reader, stdin *os.File) (client.ExposeOptions, error) {
	opts := client.ExposeOptions{HostHeader: exposeHostHeader}
	if exposeNoAuth && (exposeAuthUser != "" || exposePasswordStdin) {
		return opts, errors.New("--no-basic-auth can't be combined with --basic-auth-user or --basic-auth-password-stdin")
	}
	if exposeNoAuth {
		none := ""
		opts.BasicAuthUser = &none
		return opts, nil
	}
	if exposeAuthUser == "" {
		if exposePasswordStdin {
			return opts, errors.New("--basic-auth-password-stdin needs --basic-auth-user")
		}
		return opts, nil
	}
	if err := client.ValidateBasicAuthUser(exposeAuthUser); err != nil {
		return opts, err
	}
	pw, err := readExposePassword(os.Getenv(exposePasswordEnv), r, stdin, exposePasswordStdin)
	if err != nil {
		return opts, err
	}
	if err := client.ValidateBasicAuthPassword(pw); err != nil {
		return opts, err
	}
	user := exposeAuthUser
	opts.BasicAuthUser = &user
	opts.BasicAuthPassword = pw
	return opts, nil
}

// trimNewline drops one trailing "\n" or "\r\n", nothing more.
func trimNewline(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// readExposePassword returns the first line of r when fromStdin, else
// fromEnv when set, else a hidden, confirmed prompt on stdin's terminal.
// An explicit flag beats the environment.
func readExposePassword(fromEnv string, r io.Reader, stdin *os.File, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(r).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading password from stdin: %w", err)
		}
		pw := trimNewline(line)
		if pw == "" {
			return "", errors.New("no password on stdin")
		}
		return pw, nil
	}
	if fromEnv != "" {
		return trimNewline(fromEnv), nil
	}
	if stdin == nil || !term.IsTerminal(int(stdin.Fd())) {
		return "", errors.New("no password: pass --basic-auth-password-stdin, set " + exposePasswordEnv + ", or run this on a terminal to be asked")
	}
	fd := int(stdin.Fd())
	_, _ = fmt.Fprintf(os.Stderr, "Password for %s (input hidden): ", exposeAuthUser)
	a, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if err := client.ValidateBasicAuthPassword(string(a)); err != nil {
		return "", err
	}
	_, _ = fmt.Fprint(os.Stderr, "Repeat it: ")
	b, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading password: %w", err)
	}
	if string(a) != string(b) {
		return "", errors.New("the passwords don't match")
	}
	return string(a), nil
}

func init() {
	exposeCmd.Flags().StringVar(&exposeAuthUser, "basic-auth-user", "", "require this username (and a password, asked for) to open the URL; on an exposed port, changes the credentials")
	exposeCmd.Flags().BoolVar(&exposePasswordStdin, "basic-auth-password-stdin", false, "read the basic-auth password from the first line of stdin (wins over "+exposePasswordEnv+")")
	exposeCmd.Flags().BoolVar(&exposeNoAuth, "no-basic-auth", false, "remove the URL's username and password")
	exposeCmd.Flags().StringVar(&exposeHostHeader, "host-header", "", "Host header the app should see instead of the public hostname (e.g. localhost:3000)")
	exposeCmd.Flags().BoolVar(&exposeNoWait, "no-wait", false, "print the URL without waiting for it to start serving")
	addOutputFlag(exposeCmd)
	addOutputFlag(exposeLsCmd)
	exposeCmd.AddCommand(exposeLsCmd)
	rootCmd.AddCommand(exposeCmd)
	rootCmd.AddCommand(unexposeCmd)
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

var (
	exposeHostHeader string
	exposeNoWait     bool
)

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
  boxctl unexpose my-box 3000      # take it down

Public URLs are part of the Pro plan; port-forward is free.

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
		in, err := c.Expose(cmd.Context(), name, port, exposeHostHeader)
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
		return nil
	},
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
		return "Offline: public URLs are part of the Pro plan. The URL is kept and serves again once the account is on Pro."
	default:
		return fmt.Sprintf("Not serving yet -- it should be within a minute. Check with `boxctl expose %s`.", name)
	}
}

func printIngressTable(list []client.Ingress) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "PORT\tSTATUS\tURL\tHOST HEADER\tAGE"); err != nil {
		return err
	}
	for _, in := range list {
		hostHeader := in.HostHeader
		if hostHeader == "" {
			hostHeader = "-"
		}
		if _, err := fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", in.Port, in.Status, in.URL, hostHeader, age(in.CreatedAt)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func init() {
	exposeCmd.Flags().StringVar(&exposeHostHeader, "host-header", "", "Host header the app should see instead of the public hostname (e.g. localhost:3000)")
	exposeCmd.Flags().BoolVar(&exposeNoWait, "no-wait", false, "print the URL without waiting for it to start serving")
	addOutputFlag(exposeCmd)
	rootCmd.AddCommand(exposeCmd)
	rootCmd.AddCommand(unexposeCmd)
}

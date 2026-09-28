package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var portForwardAddress string

var portForwardCmd = &cobra.Command{
	Use:               "port-forward <name> [LOCAL:]REMOTE [...]",
	Aliases:           []string{"pf"},
	ValidArgsFunction: completeBoxName,
	Short:             "Forward local ports to ports inside a box",
	Long: `Listens on a local port and forwards every connection to a port inside
the box, like kubectl port-forward -- for reaching a dev server, database
or dashboard running in a box from your own machine. Only you can reach
it: the traffic rides your own token through boxctl.io, and the box
stays unreachable from the internet.

  boxctl port-forward my-box 3000          # localhost:3000 -> my-box:3000
  boxctl port-forward my-box 8080:80       # localhost:8080 -> my-box:80
  boxctl port-forward my-box :5432         # a free local port -> my-box:5432
  boxctl port-forward my-box 3000 5432     # several at once

Traffic through a forward counts as using the box, so it isn't
auto-paused while you're working through it. Runs until Ctrl-C.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		specs := make([]portSpec, 0, len(args)-1)
		for _, a := range args[1:] {
			spec, err := parsePortSpec(a)
			if err != nil {
				return err
			}
			specs = append(specs, spec)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()

		c := newClient()
		if err := requireRunning(ctx, c, name); err != nil {
			return err
		}

		// Bind every listener before announcing any, so a port that's
		// already taken fails the whole command up front rather than
		// leaving a partial set running.
		listeners := make([]net.Listener, 0, len(specs))
		defer func() {
			for _, l := range listeners {
				_ = l.Close()
			}
		}()
		for _, spec := range specs {
			l, err := net.Listen("tcp", net.JoinHostPort(portForwardAddress, strconv.Itoa(spec.local)))
			if err != nil {
				return fmt.Errorf("listening for %s:%d: %w", name, spec.remote, err)
			}
			listeners = append(listeners, l)
		}

		errs := make(chan error, len(specs))
		for i, spec := range specs {
			fmt.Printf("Forwarding %s -> %s:%d\n", listeners[i].Addr(), name, spec.remote)
			go func(l net.Listener, remote int) {
				errs <- serveForward(ctx, c, l, name, remote)
			}(listeners[i], spec.remote)
		}

		select {
		case <-ctx.Done():
			return nil
		case err := <-errs:
			return err
		}
	},
}

func init() {
	portForwardCmd.Flags().StringVar(&portForwardAddress, "address", "127.0.0.1", "local address to listen on (0.0.0.0 shares the forward with your network)")
	rootCmd.AddCommand(portForwardCmd)
}

type portSpec struct{ local, remote int }

// parsePortSpec reads REMOTE, LOCAL:REMOTE or :REMOTE (LOCAL 0, so the
// OS picks a free port).
func parsePortSpec(s string) (portSpec, error) {
	localStr, remoteStr, hasColon := strings.Cut(s, ":")
	if !hasColon {
		localStr, remoteStr = s, s
	}
	remote, err := strconv.Atoi(remoteStr)
	if err != nil || remote < 1 || remote > 65535 {
		return portSpec{}, fmt.Errorf("invalid port %q: remote port must be 1-65535", s)
	}
	local := 0
	if localStr != "" {
		local, err = strconv.Atoi(localStr)
		if err != nil || local < 0 || local > 65535 {
			return portSpec{}, fmt.Errorf("invalid port %q: local port must be 0-65535", s)
		}
	}
	return portSpec{local: local, remote: remote}, nil
}

// requireRunning fails fast with a hint when name isn't running, instead
// of listening happily and then failing every connection.
func requireRunning(ctx context.Context, c *client.Client, name string) error {
	vms, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.Name != name {
			continue
		}
		if vm.State != "running" {
			return fmt.Errorf("%s is %s -- run `boxctl resume %s` first", name, vm.State, name)
		}
		return nil
	}
	return fmt.Errorf("no box named %s", name)
}

// serveForward accepts on l until ctx is done, opening one tunnel per
// accepted connection. A failed tunnel only drops that one connection --
// the app inside the box may simply not be listening yet.
func serveForward(ctx context.Context, c *client.Client, l net.Listener, name string, remote int) error {
	go func() {
		<-ctx.Done()
		_ = l.Close()
	}()
	for {
		local, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accepting on %s: %w", l.Addr(), err)
		}
		go func() {
			defer func() { _ = local.Close() }()
			conn, err := c.DialPort(ctx, name, remote)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s:%d: %v\n", name, remote, err)
				return
			}
			defer func() { _ = conn.Close() }()
			if reason := relayPort(conn, local); reason != "" {
				fmt.Fprintf(os.Stderr, "%s:%d: %s\n", name, remote, reason)
			}
		}()
	}
}

// relayPort copies bytes both ways between the tunnel and the local
// connection until either side ends, mirroring boxctl-vms's
// internal/rawrelay on the far end: every binary message is payload,
// with no framing of its own. Returns the far end's reason when it
// closed abnormally -- typically that nothing inside the box is
// listening on the port.
func relayPort(conn *websocket.Conn, local net.Conn) (reason string) {
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				var ce *websocket.CloseError
				if errors.As(err, &ce) && ce.Code != websocket.CloseNormalClosure && ce.Text != "" {
					reason = ce.Text
				}
				// Unblock the other goroutine's Read so both finish.
				_ = local.Close()
				return
			}
			if _, err := local.Write(msg); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					// Tell the far end we're done so it closes the box-side
					// connection too, rather than waiting on a read.
					_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				}
				return
			}
		}
	}()
	<-done
	_ = local.Close()
	_ = conn.Close()
	<-done
	return reason
}

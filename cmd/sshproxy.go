package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

// sshProxyReadyTimeout bounds waiting for a box ssh-proxy just resumed.
const sshProxyReadyTimeout = 90 * time.Second

var sshProxyCmd = &cobra.Command{
	Use:               "ssh-proxy <name> [port]",
	ValidArgsFunction: completeBoxName,
	Short:             "Connect stdin/stdout to a port inside a box (for ssh's ProxyCommand)",
	Long: `Relays stdin and stdout to a port inside the box -- 22, the box's sshd,
unless another is given -- over the same tunnel as port-forward. It's
meant as ssh's ProxyCommand, so plain ssh (and scp, rsync, git, VS Code
Remote-SSH) reach a box with nothing listening locally:

  ssh -o ProxyCommand='boxctl ssh-proxy %h' root@my-box

or once, in ~/.ssh/config:

  Host *.box
    ProxyCommand boxctl ssh-proxy %n
    User root

The box needs your SSH public key in /root/.ssh/authorized_keys (add it
with boxctl ssh). A paused box is resumed first.

Unlike boxctl ssh, which runs a shell or one command over boxctl's own
terminal protocol, this is a real ssh connection: port and socket
forwarding, file transfer and stdin all work.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := sshProxyHost(args[0])
		port := 22
		if len(args) == 2 {
			p, err := strconv.Atoi(args[1])
			if err != nil || p < 1 || p > 65535 {
				return fmt.Errorf("invalid port %q", args[1])
			}
			port = p
		}
		c := newClient()
		if err := ensureRunning(cmd.Context(), c, name, sshProxyReadyTimeout); err != nil {
			return err
		}
		conn, err := c.DialPort(cmd.Context(), name, port)
		if err != nil {
			return fmt.Errorf("connecting to %s:%d: %w", name, port, err)
		}
		defer func() { _ = conn.Close() }()
		if reason := relayStdio(conn, os.Stdin, os.Stdout); reason != "" {
			return fmt.Errorf("%s:%d: %s", name, port, reason)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(sshProxyCmd)
}

// sshProxyHost strips a ".box" suffix, so an ssh config can match
// `Host *.box` and pass %h or %n straight through.
func sshProxyHost(host string) string {
	return strings.TrimSuffix(host, ".box")
}

// ensureRunning resumes name if it's paused and waits for it to be
// ready; a running box returns at once.
func ensureRunning(ctx context.Context, c *client.Client, name string, timeout time.Duration) error {
	vms, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.Name != name {
			continue
		}
		switch vm.State {
		case "running":
			if vm.Ready {
				return nil
			}
		case "paused":
			fmt.Fprintf(os.Stderr, "Resuming %s...\n", name)
			if _, err := c.Resume(ctx, name); err != nil {
				return fmt.Errorf("resuming %s: %w", name, err)
			}
		default:
			return fmt.Errorf("%s is %s", name, vm.State)
		}
		_, err := c.WaitReady(ctx, name, timeout)
		return err
	}
	return fmt.Errorf("no box named %s", name)
}

// relayStdio is relayPort for a pipe pair instead of a socket: stdin goes
// out as binary messages, messages come back on stdout, until the box
// side closes. stdin reaching EOF tells the far end we're done writing;
// the relay still waits for the box to finish sending.
func relayStdio(conn *websocket.Conn, in io.Reader, out io.Writer) (reason string) {
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
		}
	}()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			var ce *websocket.CloseError
			if errors.As(err, &ce) && ce.Code != websocket.CloseNormalClosure && ce.Text != "" {
				return ce.Text
			}
			return ""
		}
		if _, err := out.Write(msg); err != nil {
			return ""
		}
	}
}

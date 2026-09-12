package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// resizeMarker/resizeMessage mirror boxctl-vms's internal/ptyrelay
// exactly -- a 0x01-prefixed JSON payload on the same binary WebSocket
// stream as raw keystrokes, which is how the browser's xterm.js client
// tells the far end to resize the pty. This CLI speaks the same
// protocol directly, in place of a browser.
const resizeMarker = 0x01

type resizeMessage struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

var sshCmd = &cobra.Command{
	Use:   "ssh <name>",
	Short: "Open an interactive terminal on a box",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSSH(cmd.Context(), args[0])
	},
}

func init() {
	rootCmd.AddCommand(sshCmd)
}

func runSSH(ctx context.Context, name string) error {
	c := newClient()
	ticket, vmID, err := c.MintTerminalTicket(ctx, name)
	if err != nil {
		return fmt.Errorf("opening a terminal on %s: %w", name, err)
	}

	wsURL := strings.Replace(c.BaseURL(), "http", "ws", 1) +
		"/ws/terminal/" + url.PathEscape(vmID) + "?ticket=" + url.QueryEscape(ticket)

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("connecting to terminal: %w", err)
	}
	defer func() { _ = conn.Close() }()

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("entering raw terminal mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	sendResize(conn)
	stopResize := make(chan struct{})
	defer close(stopResize)
	go watchResize(conn, stopResize)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if _, err := os.Stdout.Write(msg); err != nil {
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Only readDone matters for knowing the session ended -- the stdin
	// goroutine above blocks on a real read with no cancellation, and
	// exiting the process (once readDone fires) is what actually stops
	// it, not the other way around.
	<-readDone
	fmt.Println()
	return nil
}

func sendResize(conn *websocket.Conn) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	payload, err := json.Marshal(resizeMessage{Cols: w, Rows: h})
	if err != nil {
		return
	}
	msg := append([]byte{resizeMarker}, payload...)
	_ = conn.WriteMessage(websocket.BinaryMessage, msg)
}

// watchResize polls the local terminal size once a second and forwards
// changes. Polling instead of a SIGWINCH handler is deliberate: SIGWINCH
// doesn't exist on Windows, and this way there's no build-tag-gated
// signal-handling code to maintain for the sake of shaving ~1s of
// resize lag.
func watchResize(conn *websocket.Conn, stop <-chan struct{}) {
	lastW, lastH, _ := term.GetSize(int(os.Stdout.Fd()))
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w, h, err := term.GetSize(int(os.Stdout.Fd()))
			if err != nil || (w == lastW && h == lastH) {
				continue
			}
			lastW, lastH = w, h
			sendResize(conn)
		}
	}
}

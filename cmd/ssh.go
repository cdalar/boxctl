package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
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

// sshTimeout bounds a non-interactive `ssh <name> -- command` (see
// runSSHCommand); irrelevant to an interactive session. boxctl-vms caps
// this at 5m server-side and silently falls back to its 30s default
// beyond that, hence the client-side check in sshCmd's RunE.
var sshTimeout time.Duration

const maxSSHTimeout = 5 * time.Minute

// sshSession is --session: 0 for a shell of its own, or one of the box's
// persistent sessions, 1..maxSSHSessions (boxctl-vms's
// ptyrelay.MaxSessions). Session 1 is the dashboard terminal's.
var sshSession int

const maxSSHSessions = 5

var sshCmd = &cobra.Command{
	Use:                   "ssh <name> [-- command [args...]]",
	ValidArgsFunction:     completeBoxName,
	Short:                 "Open an interactive terminal on a box, or run one command on it",
	DisableFlagsInUseLine: true,
	Long: `Without a command, opens an interactive terminal on the box.

With "-- command [args...]", runs that command on the box instead (no
pty -- like ssh or docker exec), relays its stdout and stderr to yours,
and exits with the command's own exit code. The words after -- are
joined with spaces and run through the box's shell, so quoting works
the way it does with ssh:

  boxctl ssh my-box -- ls -al
  boxctl ssh my-box -- "ls -la | grep foo"

Each plain terminal is a shell of its own, gone when you disconnect.
--session N (1-5) is one of the box's persistent shells instead: it
keeps running after you disconnect, and the next --session N -- from
here or the dashboard's terminal, whose shell 1 it is -- attaches to it,
every attached terminal sharing it. Exiting the shell ends it; to leave
it running, disconnect with Enter ~ . (as with ssh; ~~ sends a ~).

  boxctl ssh my-box --session 2`,
	Args: func(cmd *cobra.Command, args []string) error {
		dash := cmd.ArgsLenAtDash()
		switch {
		case len(args) < 1:
			return fmt.Errorf("requires a box name")
		case dash == -1 && len(args) > 1:
			return fmt.Errorf("accepts one box name; put a remote command after \"--\"")
		case dash > 1:
			return fmt.Errorf("accepts one box name before \"--\"")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		remote := args[1:] // everything after --, empty for a bare "ssh <name> --"
		if sshSession < 0 || sshSession > maxSSHSessions {
			return fmt.Errorf("--session must be between 1 and %d", maxSSHSessions)
		}
		if len(remote) == 0 {
			return runSSH(cmd.Context(), name, sshSession)
		}
		if sshSession != 0 {
			return fmt.Errorf("--session is for a terminal, not \"-- command\"")
		}
		if sshTimeout <= 0 || sshTimeout > maxSSHTimeout {
			return fmt.Errorf("--timeout must be between 1s and %s", maxSSHTimeout)
		}
		code, err := runSSHCommand(cmd.Context(), name, strings.Join(remote, " "))
		if err != nil {
			return err
		}
		os.Exit(code)
		return nil // unreachable
	},
}

func init() {
	sshCmd.Flags().IntVarP(&sshSession, "session", "s", 0, fmt.Sprintf("attach to (or start) the box's persistent shell N, 1-%d, which outlives the connection", maxSSHSessions))
	sshCmd.Flags().DurationVarP(&sshTimeout, "timeout", "T", 30*time.Second, "for \"-- command\": how long to let it run before it's killed (max 5m)")
	rootCmd.AddCommand(sshCmd)
}

// runSSHCommand runs command on name over the same no-pty exec endpoint
// `boxctl exec` uses, but on an existing box and as a plain relay --
// stdout to stdout, stderr to stderr, the command's exit code as the
// return value -- rather than exec's one-JSON-object contract.
func runSSHCommand(ctx context.Context, name, command string) (int, error) {
	result, err := newClient().Exec(ctx, name, command, sshTimeout)
	if err != nil {
		return 0, fmt.Errorf("running command on %s: %w", name, err)
	}
	if result.Error != "" {
		return 0, fmt.Errorf("running command on %s: %s", name, result.Error)
	}
	if _, err := os.Stdout.WriteString(result.Stdout); err != nil {
		return 0, err
	}
	if _, err := os.Stderr.WriteString(result.Stderr); err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

// terminalWSURL is the /ws/terminal URL for vmID: a shell of its own for
// session 0, else persistent session n (boxctl-vms's ?persist=1&session=N).
func terminalWSURL(baseURL, vmID, ticket string, session int) string {
	u := strings.Replace(baseURL, "http", "ws", 1) +
		"/ws/terminal/" + url.PathEscape(vmID) + "?ticket=" + url.QueryEscape(ticket)
	if session > 0 {
		u += "&persist=1&session=" + strconv.Itoa(session)
	}
	return u
}

func runSSH(ctx context.Context, name string, session int) error {
	c := newClient()
	ticket, vmID, err := c.MintTerminalTicket(ctx, name)
	if err != nil {
		return fmt.Errorf("opening a terminal on %s: %w", name, err)
	}

	wsURL := terminalWSURL(c.BaseURL(), vmID, ticket, session)

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
		var esc sshEscape
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				out, quit := esc.feed(buf[:n])
				if len(out) > 0 {
					if werr := conn.WriteMessage(websocket.BinaryMessage, out); werr != nil {
						return
					}
				}
				if quit {
					// Ends the read loop above, and so the session;
					// a persistent shell keeps running on the box.
					_ = conn.Close()
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

// sshEscape is ssh's escape character, the one part of it a terminal
// here needs: Enter ~ . disconnects, which is how a --session shell is
// left running (the box's tmux has no detach key). ~ at the start of a
// line is held back until the next byte says what it was; ~~ sends one
// ~, and ~ followed by anything else sends both.
type sshEscape struct {
	midLine bool // not at the start of a line (the zero value is)
	tilde   bool // a ~ at the start of a line, held back
}

// feed returns what of in to send, and whether to disconnect (anything
// after the ~. is dropped).
func (e *sshEscape) feed(in []byte) (out []byte, quit bool) {
	for _, b := range in {
		if e.tilde {
			e.tilde = false
			switch b {
			case '.':
				return out, true
			case '~':
				out = append(out, '~')
				e.midLine = true
				continue
			}
			out = append(out, '~')
		} else if !e.midLine && b == '~' {
			e.tilde = true
			continue
		}
		out = append(out, b)
		e.midLine = b != '\r' && b != '\n'
	}
	return out, false
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

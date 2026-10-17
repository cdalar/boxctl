package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"
)

// errConnectionLost: ssh lost its connection to the box (its exit status
// 255), as opposed to the session ending or you detaching.
var errConnectionLost = errors.New("connection to the box lost")

// reconnectProbeTimeout bounds one check whether the box is reachable
// again, so a connection that hangs rather than fails is retried.
const reconnectProbeTimeout = 20 * time.Second

// reconnectMaxDelay caps the wait between checks.
const reconnectMaxDelay = 15 * time.Second

// reattach runs attach, and again each time the connection drops under it
// (on a train, through a tunnel), once the box is reachable again and the
// session still running there; Claude runs in tmux, so it never noticed.
// It returns attach's result when the session ends or you detach, nil if
// the session ended while the connection was down, and errConnectionLost
// if you gave up waiting (Ctrl-C).
func reattach(ctx context.Context, box *boxSSH, sess taskSession, attach func() error) error {
	for {
		err := attach()
		if !errors.Is(err, errConnectionLost) {
			return err
		}
		running, err := awaitBox(ctx, box, sess)
		if err != nil {
			return errConnectionLost
		}
		if !running {
			sayNote("%s ended on %s while the connection was down", agent.title, box.name)
			return nil
		}
		sayDone("Reconnected to %s", bold(box.name))
	}
}

// awaitBox waits until the box can be reached again and reports whether
// sess is still running there. It stops waiting only when ctx is
// cancelled.
func awaitBox(ctx context.Context, box *boxSSH, sess taskSession) (running bool, err error) {
	// The shared connection rode the same tunnel: start a new one.
	box.close()
	sp := startSpinner(fmt.Sprintf("Lost the connection to %s -- reconnecting (Ctrl-C to stop)", bold(box.name)))
	delay := time.Second
	for {
		probe, cancel := context.WithTimeout(ctx, reconnectProbeTimeout)
		running, err = sessionAlive(probe, box, sess)
		cancel()
		if err == nil {
			sp.end("")
			return running, nil
		}
		select {
		case <-ctx.Done():
			sp.fail("Stopped reconnecting to " + bold(box.name))
			return false, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, reconnectMaxDelay)
	}
}

// sessionAlive reports whether sess is running on the box, or an error if
// the box can't be reached. Quietly: ssh's and the tunnel's complaints
// would only repeat, under the spinner, that the network is down.
func sessionAlive(ctx context.Context, box *boxSSH, sess taskSession) (bool, error) {
	argv := append(append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}, box.args...),
		"tmux has-session -t ="+sess.tmux+" 2>/dev/null")
	err := exec.CommandContext(ctx, "ssh", argv...).Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() != 255 {
		return false, nil // reached the box; no such session
	}
	return err == nil, err
}

// terminalReset turns off what a full-screen program on the box (Claude,
// tmux) turns on in your terminal and couldn't turn off when the
// connection dropped under it: mouse reporting -- left on, moving the
// mouse types escape codes like 35;94;27M at your prompt -- focus events
// and bracketed paste; and leaves the alternate screen, cursor shown.
const terminalReset = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1015l" +
	"\x1b[?1004l\x1b[?2004l\x1b[?1049l\x1b[?25h"

// resetTerminal writes terminalReset, if stdout is a terminal.
func resetTerminal() {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		_, _ = os.Stdout.WriteString(terminalReset)
	}
}

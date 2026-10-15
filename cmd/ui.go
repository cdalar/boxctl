package cmd

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

// Terminal niceties for the human-facing commands (colour, a spinner).
// Everything here degrades to the old plain text when stdout isn't a
// terminal or NO_COLOR is set, so pipes, scripts and CI logs are unchanged.

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// paint wraps s in an ANSI style, when stdout supports colour.
func paint(style, s string) string {
	if !useColor() {
		return s
	}
	return style + s + ansiReset
}

func bold(s string) string { return paint(ansiBold, s) }
func dim(s string) string  { return paint(ansiDim, s) }
func cyan(s string) string { return paint(ansiCyan, s) }
func okMark() string       { return paint(ansiGreen, "✔") }
func failMark() string     { return paint(ansiRed, "✘") }

func stateText(s string) string {
	switch s {
	case "running":
		return paint(ansiGreen, s)
	case "paused":
		return paint(ansiYellow, s)
	case "provisioning", "stopping":
		return paint(ansiCyan, s)
	}
	return s
}

// isTTY reports whether stdout is an interactive terminal, i.e. whether
// redrawing a line in place makes sense.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner is an animated "⠋ message" line. On a non-terminal it just
// prints "message..." once, like the plain output it replaces.
type spinner struct {
	mu   sync.Mutex
	msg  string
	tty  bool
	stop chan struct{}
	done chan struct{}
}

// startSpinner begins animating msg; call finish (or fail) exactly once.
func startSpinner(msg string) *spinner {
	s := &spinner{msg: msg, tty: isTTY() && os.Getenv("TERM") != "dumb"}
	setLiveSpinner(s)
	if !s.tty {
		fmt.Println(msg + "...")
		return s
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			s.mu.Lock()
			s.draw(i)
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-t.C:
			}
		}
	}()
	return s
}

func (s *spinner) draw(frame int) {
	fmt.Printf("\r\033[K%s %s", cyan(spinnerFrames[frame%len(spinnerFrames)]), s.msg)
}

// println prints a line above the spinner without garbling it.
func (s *spinner) println(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tty {
		fmt.Print("\r\033[K")
	}
	fmt.Println(line)
}

// setMsg changes the text next to the spinner.
func (s *spinner) setMsg(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}

// end stops the animation and replaces the line with final (if any).
func (s *spinner) end(final string) {
	setLiveSpinner(nil)
	if s.tty {
		close(s.stop)
		<-s.done
		s.mu.Lock()
		fmt.Print("\r\033[K")
		s.mu.Unlock()
	}
	if final != "" {
		fmt.Println(final)
	}
}

func (s *spinner) finish(final string) { s.end(okMark() + " " + final) }
func (s *spinner) fail(final string)   { s.end(failMark() + " " + final) }

// liveSpin is the spinner currently animating, so code far from where it
// was started (an upload's progress, a step finishing in a goroutine) can
// print above it or change its text without it being passed around.
var (
	liveMu   sync.Mutex
	liveSpin *spinner
)

func setLiveSpinner(s *spinner) {
	liveMu.Lock()
	liveSpin = s
	liveMu.Unlock()
}

func currentSpinner() *spinner {
	liveMu.Lock()
	defer liveMu.Unlock()
	return liveSpin
}

// say prints a status line above the running spinner, or on stderr when
// there is none (stdout is the session's, for the commands that attach).
func say(line string) {
	if s := currentSpinner(); s != nil {
		s.println(line)
		return
	}
	fmt.Fprintln(os.Stderr, line)
}

// sayDone prints a finished step: "✔ message".
func sayDone(format string, a ...any) { say(okMark() + " " + fmt.Sprintf(format, a...)) }

// sayNote prints a quiet aside: "• message", dimmed.
func sayNote(format string, a ...any) { say(dim("• " + fmt.Sprintf(format, a...))) }

// sayWarn prints a warning: "! message", in yellow.
func sayWarn(format string, a ...any) {
	say(paint(ansiYellow, "!") + " " + fmt.Sprintf(format, a...))
}

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

// end stops the animation and replaces the line with final (if any).
func (s *spinner) end(final string) {
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

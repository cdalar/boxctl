package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"
)

// ls never waits for GitHub: the listing is printed from the server's
// answer alone, and each pull request's state -- open, merged or
// closed -- is then written after its URL on the screen as `gh pr
// view` reports it, with cursor movement over the lines just printed.
// So only on a terminal, and only for lines still on it: piped or JSON
// output never shows a state, and a line that scrolled off (or one
// below it too wide, which the terminal would wrap, shifting every
// offset above) is left as printed. `gh` missing, signed out, or slower
// than prStateTimeout leaves the URL as printed too, with one note on
// stderr when nothing came back.

// prCell locates one box's pull request on its run line for the fill-in.
type prCell struct {
	up    int    // lines above the cursor's, once everything is printed
	width int    // the widest line from the run line's down to the cursor's (in runes)
	col   int    // the URL's first column, 0-based (in runes)
	url   string // the first pull request's
	rest  string // what followed it: " +1" for a workspace's other PRs
}

// prStateTimeout bounds how long ls stays around for GitHub.
const prStateTimeout = 10 * time.Second

// fillPRStates looks each cell's pull request up and rewrites the line
// from the URL on as answers arrive.
func fillPRStates(ctx context.Context, cells []prCell) {
	if len(cells) == 0 || !isTTY() || os.Getenv("TERM") == "dumb" {
		return
	}
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return
	}
	cells = slices.DeleteFunc(slices.Clone(cells), func(c prCell) bool { return c.width >= cols })
	if len(cells) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, prStateTimeout)
	defer cancel()
	type result struct {
		i     int
		state string
		err   error
	}
	results := make(chan result)
	sem := make(chan struct{}, 4)
	for i, c := range cells {
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			state, err := prState(ctx, c.url)
			results <- result{i, state, err}
		}()
	}
	var firstErr error
	got := 0
	for range cells {
		r := <-results
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		got++
		c := cells[r.i]
		if c.up >= rows {
			continue // scrolled off the screen
		}
		// Up to the line, over to the URL, rewrite it to the end of
		// the line (the pull request is the last part), back down.
		fmt.Printf("\033[%dA\033[%dG%s %s%s\033[K\033[%dB\r", c.up, c.col+1, c.url, prStateText(r.state), c.rest, c.up)
	}
	if got == 0 && firstErr != nil {
		fmt.Fprintln(os.Stderr, dim("PR state not shown: "+firstErr.Error()))
	}
}

// prState is url's pull request's state from `gh pr view`: OPEN,
// MERGED or CLOSED.
func prState(ctx context.Context, url string) (string, error) {
	var stderr strings.Builder
	c := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json", "state")
	c.Stderr = &stderr
	out, err := c.Output()
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return "", errors.New("gh isn't installed")
	case ctx.Err() != nil:
		return "", fmt.Errorf("GitHub didn't answer in %s", prStateTimeout)
	case err != nil:
		if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
			return "", errors.New(line)
		}
		return "", err
	}
	return parsePRState(out)
}

// parsePRState reads `gh pr view --json state`'s answer.
func parsePRState(out []byte) (string, error) {
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("reading gh's answer: %w", err)
	}
	if v.State == "" {
		return "", errors.New("gh reported no state")
	}
	return v.State, nil
}

// prStateText is the state word as the PR column shows it: merged in
// green, closed in red, open (and anything new) plain.
func prStateText(state string) string {
	word := strings.ToLower(state)
	switch state {
	case "MERGED":
		return paint(ansiGreen, word)
	case "CLOSED":
		return paint(ansiRed, word)
	}
	return word
}

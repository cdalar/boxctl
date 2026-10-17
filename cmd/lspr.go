package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cdalar/boxctl/internal/client"
	"golang.org/x/term"
)

// ls never waits for GitHub: the table is printed from the server's
// answer alone, and the PR column is then filled in on the screen as
// `gh pr view` reports each pull request's state -- open, merged or
// closed -- with cursor movement over the lines just printed. So only
// on a terminal, and only for rows still on it: piped or JSON output
// never shows a state, and a row that scrolled off (or a table too
// wide, which would wrap and shift every offset) is left as printed.
// `gh` missing, signed out, or slower than prStateTimeout leaves the
// column as printed too, with one note on stderr when nothing came back.

// prCell locates one row's PR cell for the fill-in.
type prCell struct {
	up   int    // lines above the cursor's, once everything is printed
	col  int    // the cell's first column, 0-based (in runes, like tabwriter's widths)
	num  string // "#12"
	rest string // what followed it: " +1" for a workspace's other PRs
	url  string // the first pull request's
}

// prStateTimeout bounds how long ls stays around for GitHub.
const prStateTimeout = 10 * time.Second

// prCellOf is vm's PR cell in its plain (uncoloured) aligned row, or
// false when the row has no pull request.
func prCellOf(vm client.VM, row string) (prCell, bool) {
	st, _ := parseGoalStatus(vm.RunStatus)
	if st == nil {
		return prCell{}, false
	}
	m := prNumber.FindStringSubmatch(st.PRURL)
	if m == nil {
		return prCell{}, false
	}
	text := prText(vm)
	if !strings.HasSuffix(row, text) {
		return prCell{}, false
	}
	num, rest, _ := strings.Cut(text, " ")
	if rest != "" {
		rest = " " + rest
	}
	return prCell{
		col:  utf8.RuneCountInString(row) - len(text),
		num:  num,
		rest: rest,
		url:  st.PRURL,
	}, true
}

// fillPRStates looks each cell's pull request up and rewrites the cell
// in place as answers arrive. width is the widest line printed since
// the table's first: one the terminal wrapped would shift every offset.
func fillPRStates(ctx context.Context, cells []prCell, width int) {
	if len(cells) == 0 || !isTTY() || os.Getenv("TERM") == "dumb" {
		return
	}
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width >= cols {
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
		// Up to the row, over to the cell, rewrite it to the end of
		// the line (PR is the last column), back down.
		fmt.Printf("\033[%dA\033[%dG%s %s%s\033[K\033[%dB\r", c.up, c.col+1, c.num, prStateText(r.state), c.rest, c.up)
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

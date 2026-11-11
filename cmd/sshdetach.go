package cmd

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// `boxctl ssh <box> --detach -- cmd` / `--wait [--follow]`: a command
// longer than the exec endpoint's 5m cap, without a connection held open
// for it. Both halves are short Client.Exec calls: --detach starts the
// command under nohup/setsid in ~/.boxctl/detached/<id>/ on the box
// (cmd.sh, log, and status once it ends, holding its exit code; "latest"
// points at the newest), --wait polls that directory every
// detachPollInterval.

var (
	sshDetach bool
	sshWait   bool
	sshFollow bool
)

// detachPollInterval paces --wait's polls; a variable so tests don't wait.
var detachPollInterval = 2 * time.Second

// detachChunk caps the log bytes one --follow poll fetches, under the
// exec endpoint's 256 KiB per-stream cap.
const detachChunk = 128 * 1024

const detachStartScript = `d="$HOME/.boxctl/detached"
id="$(date +%%Y%%m%%d-%%H%%M%%S)-$$"
mkdir -p "$d/$id" || exit 1
printf %%s '%s' | base64 -d > "$d/$id/cmd.sh" || exit 1
s="$d/$id"
run='sh "$1/cmd.sh"; echo $? > "$1/status.tmp"; mv "$1/status.tmp" "$1/status"'
if command -v setsid >/dev/null 2>&1; then
  nohup setsid sh -c "$run" sh "$s" >"$s/log" 2>&1 </dev/null &
else
  nohup sh -c "$run" sh "$s" >"$s/log" 2>&1 </dev/null &
fi
ln -sfn "$id" "$d/latest"
echo "$s"
`

// detachStartCmd is the script that starts command detached; it prints
// the run's directory on the box.
func detachStartCmd(command string) string {
	return fmt.Sprintf(detachStartScript, base64.StdEncoding.EncodeToString([]byte(command)))
}

// detachPollCmd reports the last detached command: first line "running"
// or "done <exit>" (read before the log, so a "done" log is complete),
// then, when follow, up to detachChunk log bytes from byte offset on.
func detachPollCmd(follow bool, offset int) string {
	s := `s="$HOME/.boxctl/detached/latest"
[ -d "$s/" ] || { echo none; exit 0; }
if [ -f "$s/status" ]; then echo "done $(cat "$s/status")"; else echo running; fi
`
	if follow {
		s += fmt.Sprintf(`tail -c +%d "$s/log" | head -c %d
`, offset+1, detachChunk)
	}
	return s
}

func runSSHDetach(ctx context.Context, name, command string) error {
	c := newClient()
	res, err := c.Exec(ctx, name, detachStartCmd(command), 30*time.Second)
	if err != nil {
		return fmt.Errorf("starting command on %s: %w", name, err)
	}
	if res.Error != "" || res.ExitCode != 0 {
		return fmt.Errorf("starting command on %s: %s%s", name, res.Error, res.Stderr)
	}
	dir := strings.TrimSpace(res.Stdout)
	fmt.Printf("Started on %s; output in %s/log\n", name, dir)
	fmt.Printf("  follow it and get its exit status:  boxctl ssh %s --wait --follow\n", name)
	fmt.Printf("  just wait for it:                    boxctl ssh %s --wait\n", name)
	fmt.Printf("  read the log so far:                 boxctl ssh %s -- tail %s/log\n", name, dir)
	return nil
}

// runSSHWait waits for the box's last detached command and returns its
// exit status, relaying its log meanwhile when follow.
func runSSHWait(ctx context.Context, name string, follow bool) (int, error) {
	c := newClient()
	offset := 0
	for {
		res, err := c.Exec(ctx, name, detachPollCmd(follow, offset), 30*time.Second)
		if err != nil {
			return 0, fmt.Errorf("waiting on %s: %w", name, err)
		}
		if res.Error != "" || res.ExitCode != 0 {
			return 0, fmt.Errorf("waiting on %s: %s%s", name, res.Error, res.Stderr)
		}
		state, body, _ := strings.Cut(res.Stdout, "\n")
		if state == "none" {
			return 0, fmt.Errorf("no detached command on %s (start one with \"boxctl ssh %s --detach -- command\")", name, name)
		}
		if follow && body != "" {
			if _, err := os.Stdout.WriteString(body); err != nil {
				return 0, err
			}
			offset += len(body)
			if len(body) >= detachChunk {
				continue // more log waiting; don't sleep
			}
		}
		if code, ok := strings.CutPrefix(state, "done "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(code))
			if err != nil {
				return 0, fmt.Errorf("unreadable exit status %q", code)
			}
			return n, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(detachPollInterval):
		}
	}
}

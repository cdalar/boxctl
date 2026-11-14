package cmd

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/boxctl-io/boxctl-cli/internal/client"
)

// `boxctl ssh <box> --detach -- cmd` / `--wait [id] [--follow]`: a command
// longer than the exec endpoint's 5m cap, without a connection held open
// for it. Both halves are short Client.Exec calls: --detach starts the
// command under nohup/setsid in ~/.boxctl/detached/<id>/ on the box (cmd.sh,
// run.sh -- the wrapper --, log, pid, and status once it ends, holding its
// exit code; "latest" points at the newest), --wait polls that directory
// every detachPollInterval.
//
// The wrapper alone writes status, so --wait also checks that the
// wrapper's process (pid plus its start time and the boot id, so a reused
// pid or a reboot is not mistaken for it) is still alive: without that a
// killed wrapper, an OOM kill or a reboot would leave --wait polling
// "running" forever.

// Exit codes of `ssh --wait` when it is the wait that ended, not the
// command (whose own exit code is passed through, so these two can in
// principle also be a command's). Documented in the help and README.
const (
	exitWaitFailed = 250 // the wait itself failed: box or API unreachable, no such run, paused box
	exitRunLost    = 251 // the command is gone and left no exit status
)

var (
	sshDetach  bool
	sshWait    bool
	sshFollow  bool
	sshIdleTTL time.Duration
)

// Variables so tests don't wait.
var (
	detachPollInterval = 2 * time.Second
	// A failed poll is retried with backoff, doubling from detachRetryBase
	// to detachRetryMax, until detachRetryFor has passed without a
	// successful one.
	detachRetryBase = time.Second
	detachRetryMax  = 15 * time.Second
	detachRetryFor  = 5 * time.Minute
)

// detachChunk caps the log bytes one --follow poll fetches; base64 makes
// that 4/3 as many, under the exec endpoint's 256 KiB per-stream cap.
const detachChunk = 128 * 1024

// detachKeep is how many runs' directories stay on the box; --detach
// removes older finished (or dead) ones.
const detachKeep = 20

var detachIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9]+$`)

// detachCommon defines alive DIR: the process recorded in DIR/pid is
// still the wrapper that was started (same boot, same start time, not a
// zombie).
const detachCommon = `bootid() { cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo -; }
alive() {
  [ -s "$1/pid" ] || return 1
  read -r p t b 2>/dev/null < "$1/pid" || return 1
  [ "$b" = "$(bootid)" ] || return 1
  if [ -r "/proc/$p/stat" ]; then
    f=$(sed 's/.*) //' "/proc/$p/stat" 2>/dev/null)
    [ "$(echo "$f" | cut -d' ' -f1)" != Z ] || return 1
    [ "$t" = - ] || [ "$(echo "$f" | cut -d' ' -f20)" = "$t" ]
  else
    kill -0 "$p" 2>/dev/null
  fi
}
`

// detachStartScript's @B64@ is the command. It prints the run's id and
// directory, and fails (saying why) if the wrapper never recorded its pid.
const detachStartScript = detachCommon + `d="$HOME/.boxctl/detached"
id="$(date +%Y%m%d-%H%M%S)-$$"
s="$d/$id"
mkdir -p "$s" || exit 1
printf %s '@B64@' | base64 -d > "$s/cmd.sh" || exit 1
cat > "$s/run.sh" <<'EOS'
d=$1
st=$(sed 's/.*) //' "/proc/$$/stat" 2>/dev/null | cut -d' ' -f20)
echo "$$ ${st:--} $(cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo -)" > "$d/pid.tmp" && mv "$d/pid.tmp" "$d/pid"
sh_=$(getent passwd "$(id -u)" 2>/dev/null | cut -d: -f7)
{ [ -n "$sh_" ] && [ -x "$sh_" ] && grep -qx "$sh_" /etc/shells 2>/dev/null; } || sh_=/bin/sh
"$sh_" -c "$(cat "$d/cmd.sh")"
echo $? > "$d/status.tmp"
mv "$d/status.tmp" "$d/status"
EOS
n=""; command -v nohup >/dev/null 2>&1 && n=nohup
ss=""; command -v setsid >/dev/null 2>&1 && ss=setsid
$n $ss sh "$s/run.sh" "$s" >"$s/log" 2>&1 </dev/null &
i=0
while [ ! -s "$s/pid" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i+1)); done
if [ ! -s "$s/pid" ]; then
  echo "the command did not start:" >&2
  cat "$s/log" >&2
  exit 1
fi
ln -sfn "$id" "$d/latest"
ls -1 "$d" | grep -E '^[0-9]{8}-[0-9]{6}-[0-9]+$' | sort -r | tail -n +@KEEP@ | while read -r old; do
  if [ -f "$d/$old/status" ] || ! alive "$d/$old"; then rm -rf "$d/$old"; fi
done
echo "$id"
echo "$s"
`

// detachStartCmd is the script that starts command detached.
func detachStartCmd(command string) string {
	r := strings.NewReplacer("@B64@", base64.StdEncoding.EncodeToString([]byte(command)), "@KEEP@", strconv.Itoa(detachKeep+1))
	return r.Replace(detachStartScript)
}

// detachPollCmd reports run id ("" or "latest": the newest): first line
// "running", "done <exit>" or "lost" (the wrapper is gone without a
// status), read before the log so a finished run's log is complete, then,
// when follow, up to detachChunk log bytes from byte offset on as one
// base64 line (so the stream is byte-exact whatever the log holds).
func detachPollCmd(id string, follow bool, offset int) string {
	if id == "" {
		id = "latest"
	}
	s := detachCommon + fmt.Sprintf(`s="$HOME/.boxctl/detached/%s"
[ -d "$s/" ] || { echo none; exit 0; }
if [ -f "$s/status" ]; then echo "done $(cat "$s/status")"
elif alive "$s"; then echo running
elif [ -f "$s/status" ]; then echo "done $(cat "$s/status")"
else echo lost; fi
`, id)
	if follow {
		s += fmt.Sprintf(`tail -c +%d "$s/log" | head -c %d | base64 | tr -d '\n'
echo
`, offset+1, detachChunk)
	}
	return s
}

// boxStateHint turns a refusal because the box isn't running into
// something to act on: a paused box (the idle reaper's doing) says so.
func boxStateHint(ctx context.Context, c *client.Client, name string, err error) error {
	if client.HTTPStatus(err) != http.StatusConflict {
		return err
	}
	vms, lerr := c.List(ctx)
	if lerr != nil {
		return err
	}
	for _, vm := range vms {
		if vm.Name == name && vm.State == "paused" {
			return fmt.Errorf("%s is paused (a detached command doesn't keep a box awake, so its idle TTL paused it): resume it with \"boxctl resume %s\", then \"boxctl ssh %s --wait\" again; give the next --detach a longer --idle-ttl", name, name, name)
		}
	}
	return err
}

func runSSHDetach(ctx context.Context, name, command string, w io.Writer) error {
	c := newClient()
	if sshIdleTTL > 0 {
		if _, err := c.SetIdleTTL(ctx, name, sshIdleTTL); err != nil {
			return fmt.Errorf("setting %s's idle TTL: %w", name, boxStateHint(ctx, c, name, err))
		}
	}
	res, err := c.Exec(ctx, name, detachStartCmd(command), 30*time.Second)
	if err != nil {
		return fmt.Errorf("starting command on %s: %w", name, boxStateHint(ctx, c, name, err))
	}
	if res.Error != "" || res.ExitCode != 0 {
		return fmt.Errorf("starting command on %s: %s%s", name, res.Error, res.Stderr)
	}
	lines := strings.Fields(res.Stdout)
	if len(lines) < 2 || !detachIDRe.MatchString(lines[0]) {
		return fmt.Errorf("starting command on %s: unexpected answer %q", name, res.Stdout)
	}
	id, dir := lines[0], lines[1]
	_, _ = fmt.Fprintf(w, "Started run %s on %s; output in %s/log\n", id, name, dir)
	_, _ = fmt.Fprintf(w, "  follow it and get its exit status:  boxctl ssh %s --wait --follow\n", name)
	_, _ = fmt.Fprintf(w, "  this run, if another is started:    boxctl ssh %s --wait %s\n", name, id)
	_, _ = fmt.Fprintf(w, "  read the log so far:                 boxctl ssh %s -- tail %s/log\n", name, dir)
	if sshIdleTTL > 0 {
		_, _ = fmt.Fprintf(w, "Note: the command doesn't count as use of the box; it pauses after %s without API activity (--wait's polling counts), pausing the command with it.\n", sshIdleTTL)
	} else {
		_, _ = fmt.Fprintf(w, "Note: the command doesn't count as use of the box; it pauses after its idle TTL (1h unless changed) without API activity (--wait's polling counts), pausing the command with it. Extend with --idle-ttl on --detach (e.g. --idle-ttl 6h).\n")
	}
	return nil
}

// pollExec is one Exec call of --wait, retried with backoff while the
// failure is transient (network, 5xx -- a deploy restarting the API --
// or a box that is not ready), up to detachRetryFor.
func pollExec(ctx context.Context, c *client.Client, name, script string) (*client.ExecResult, error) {
	deadline := time.Now().Add(detachRetryFor)
	delay := detachRetryBase
	for {
		res, err := c.Exec(ctx, name, script, 30*time.Second)
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		retry := client.IsTransient(err)
		if client.HTTPStatus(err) == http.StatusConflict {
			if herr := boxStateHint(ctx, c, name, err); herr != err {
				return nil, herr // paused: not going to come back on its own
			}
			retry = true // starting up or otherwise not ready yet
		}
		if !retry {
			return nil, err
		}
		if time.Now().Add(delay).After(deadline) {
			return nil, fmt.Errorf("still failing after %s of retries: %w", detachRetryFor, err)
		}
		fmt.Fprintf(os.Stderr, "poll failed (%v); retrying in %s\n", err, delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > detachRetryMax {
			delay = detachRetryMax
		}
	}
}

// runSSHWait waits for run id ("" is the box's latest) and returns its
// exit status, relaying its log to out meanwhile when follow. When the
// wait itself ends, not the command, the code is exitWaitFailed or
// exitRunLost and err says why.
func runSSHWait(ctx context.Context, name, id string, follow bool, out io.Writer) (int, error) {
	if id != "" && id != "latest" && !detachIDRe.MatchString(id) {
		return exitWaitFailed, fmt.Errorf("%q is not a run id (--detach printed one, like 20250101-120000-1234)", id)
	}
	c := newClient()
	offset := 0
	for {
		res, err := pollExec(ctx, c, name, detachPollCmd(id, follow, offset))
		if err != nil {
			return exitWaitFailed, fmt.Errorf("waiting on %s: %w", name, err)
		}
		if res.Error != "" || res.ExitCode != 0 {
			return exitWaitFailed, fmt.Errorf("waiting on %s: %s%s", name, res.Error, res.Stderr)
		}
		state, body, _ := strings.Cut(res.Stdout, "\n")
		if state == "none" {
			return exitWaitFailed, fmt.Errorf("no detached command on %s (start one with \"boxctl ssh %s --detach -- command\")", name, name)
		}
		more := false
		if follow {
			chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
			if err != nil {
				return exitWaitFailed, fmt.Errorf("waiting on %s: unreadable log chunk: %w", name, err)
			}
			if len(chunk) > 0 {
				if _, err := out.Write(chunk); err != nil {
					return exitWaitFailed, err
				}
				offset += len(chunk)
				more = len(chunk) >= detachChunk // more log waiting; don't sleep
			}
		}
		if !more {
			if code, ok := strings.CutPrefix(state, "done "); ok {
				n, err := strconv.Atoi(strings.TrimSpace(code))
				if err != nil {
					return exitWaitFailed, fmt.Errorf("unreadable exit status %q", code)
				}
				return n, nil
			}
			if state == "lost" {
				return exitRunLost, errors.New("the command is no longer running and left no exit status (it was killed, ran out of memory, or the box rebooted); see its log on the box")
			}
			select {
			case <-ctx.Done():
				return exitWaitFailed, ctx.Err()
			case <-time.After(detachPollInterval):
			}
		}
	}
}

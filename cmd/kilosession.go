package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// A local Kilo session, found and copied to a box the way
// docs/plans/kilo-handoff.md describes. Kilo keeps every session in one
// SQLite database that also holds its credentials, so nothing here copies
// a file of ~/.local/share/kilo: a session travels as `kilo export <id>`
// JSON, which the box's `kilo import` turns back into a session.

// kiloSessionIDPattern is what a Kilo session ID, or the start of one,
// looks like (ses_e2f83c3baffeP7MEXP6xfGbwvL). It's checked before the ID
// goes into a query or a file name.
var kiloSessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]*$`)

// kiloHandoffDir is where on the box the exported session waits for
// `kilo import`: tmpfs, like the credentials' env file.
var kiloHandoffDir = muxRunDir

// kiloSession is a local session as findKiloSession found it.
type kiloSession struct {
	ID        string
	Directory string // where it was started, as Kilo recorded it
	Title     string
	JSON      []byte // `kilo export` of it, checked by checkKiloExport
}

// kiloOutput runs the local kilo with args; stdout is returned and
// stderr (Kilo's progress lines) kept for the error.
func kiloOutput(ctx context.Context, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "kilo", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("kilo isn't installed here (https://kilo.ai/cli/install)")
		}
		if msg := strings.TrimSpace(stripANSI(stderr.String())); msg != "" {
			return nil, fmt.Errorf("kilo %s: %s", args[0], lastLine(msg))
		}
		return nil, fmt.Errorf("kilo %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// findKiloSession locates the local session id -- the whole ID or a start
// of it that only one session has -- in every project's sessions, and
// exports it. An ID nothing has, or a start several sessions share, is an
// error that says so. dataDir is Kilo's data directory (kiloDirs): its
// auth.json is what the export is checked against.
func findKiloSession(ctx context.Context, dataDir, id string) (*kiloSession, error) {
	if len(id) < len("ses_")+1 || !kiloSessionIDPattern.MatchString(id) {
		return nil, fmt.Errorf("%q isn't a Kilo session ID", id)
	}
	// substr, not LIKE: "_" is a wildcard there, and every ID has one.
	query := fmt.Sprintf("select id, directory, title from session where substr(id, 1, %d) = '%s' order by time_updated desc", len(id), id)
	out, err := kiloOutput(ctx, "db", query, "--format", "json")
	if err != nil {
		return nil, err
	}
	var rows []struct{ ID, Directory, Title string }
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("reading Kilo's session list: %w", err)
	}
	switch len(rows) {
	case 0:
		return nil, fmt.Errorf("no Kilo session %s here (kilo db path: %s)", id, kiloDBPath(ctx))
	case 1:
	default:
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		sort.Strings(ids)
		return nil, fmt.Errorf("%q matches %d Kilo sessions (%s): give more of the ID", id, len(rows), strings.Join(ids, ", "))
	}
	s := &kiloSession{ID: rows[0].ID, Directory: rows[0].Directory, Title: rows[0].Title}
	if s.JSON, err = kiloOutput(ctx, "export", s.ID); err != nil {
		return nil, err
	}
	secrets, err := kiloSecrets(dataDir)
	if err != nil {
		return nil, err
	}
	if s.JSON, err = checkKiloExport(s.JSON, s.ID, secrets); err != nil {
		return nil, err
	}
	return s, nil
}

func kiloDBPath(ctx context.Context) string {
	out, err := kiloOutput(ctx, "db", "path")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// kiloSecrets are the credential values this machine's Kilo has saved
// (auth.json's strings of 8 characters or more), for checkKiloExport to
// look for.
func kiloSecrets(dataDir string) ([]string, error) {
	p := dataDir + string(os.PathSeparator) + "auth.json"
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	var secrets []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if len(x) >= 8 {
				secrets = append(secrets, x)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return secrets, nil
}

// checkKiloExport vets `kilo export` output before it goes to a box. It
// must be the session's own {"info", "messages"} and nothing else, and no
// credential of secrets may appear in it: Kilo's export carries none, but
// what leaves this machine is checked, not assumed. Anything unexpected is
// refused, not edited -- the transcript goes as it is or not at all. The
// data returned is the export, re-encoded without the keys it checked.
func checkKiloExport(data []byte, id string, secrets []string) ([]byte, error) {
	var export map[string]json.RawMessage
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("kilo export %s: not JSON: %w", id, err)
	}
	for key := range export {
		if key != "info" && key != "messages" {
			return nil, fmt.Errorf("kilo export %s has an unexpected %q section; not copying it to a box", id, key)
		}
	}
	var info struct{ ID string }
	if err := json.Unmarshal(export["info"], &info); err != nil || info.ID != id {
		return nil, fmt.Errorf("kilo export %s isn't that session", id)
	}
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			return nil, fmt.Errorf("session %s contains a credential saved in Kilo's auth.json; not copying it to a box", id)
		}
		if enc, err := json.Marshal(secret); err == nil && bytes.Contains(data, bytes.Trim(enc, `"`)) {
			return nil, fmt.Errorf("session %s contains a credential saved in Kilo's auth.json; not copying it to a box", id)
		}
	}
	return data, nil
}

// importKiloScript is the box side: the JSON arrives on stdin into a
// private file under dir (tmpfs), `kilo import` runs from the project
// directory -- it takes the session's project and directory from where it
// runs -- and the file is removed whatever happens.
func importKiloScript(projectDir, id string) string {
	file := kiloHandoffDir + "/handoff-" + id + ".json"
	return "umask 077 && mkdir -p " + shellQuote(kiloHandoffDir) + " && cat >" + shellQuote(file) +
		` && export PATH="$HOME/` + kiloAgent.binDir + `:$PATH"` +
		" && cd " + shellQuote(projectDir) + " && { kilo import " + shellQuote(file) + "; rc=$?; }; rm -f " + shellQuote(file) + "; exit ${rc:-1}"
}

// copyKiloSession copies session s (from findKiloSession) to the box, as
// session s.ID in the project at dir -- the same path as here -- so
// `kilo --session <id>` there picks the conversation up. Only the exported
// session goes, over ssh's stdin into a tmpfs file that's removed after
// the import; Kilo's database and credentials never do. The box needs
// Kilo installed and the project in place first. Importing again replaces
// the box's copy.
func copyKiloSession(ctx context.Context, box *boxSSH, dir string, s *kiloSession) error {
	fmt.Fprintf(os.Stderr, "Copying this session (%s) to %s...\n", s.ID, box.name)
	if err := box.run(ctx, importKiloScript(dir, s.ID), bytes.NewReader(s.JSON)); err != nil {
		return fmt.Errorf("importing session %s on %s: %w", s.ID, box.name, err)
	}
	return nil
}

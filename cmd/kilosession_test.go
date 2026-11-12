package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testKiloID = "ses_e2eb4bbeeffeUKt4VcJWsgIPRL"

// fakeKilo puts a kilo on PATH. `db` prints dbRows (the JSON of the
// session query) and records the query; `export <id>` prints exports[id]
// or fails like Kilo does for a session it doesn't have.
func fakeKilo(t *testing.T, dbRows string, exports map[string]string) (queryFile string) {
	t.Helper()
	dir := t.TempDir()
	queryFile = filepath.Join(dir, "query")
	script := "#!/bin/sh\ncase $1 in\ndb) if [ \"$2\" = path ]; then echo /x/kilo.db; exit 0; fi; printf %s \"$2\" >" + queryFile + "; cat " + dir + "/rows ;;\nexport) echo \"Exporting session: $2\" >&2\ncase $2 in\n"
	write(t, filepath.Join(dir, "rows"), dbRows)
	for id, out := range exports {
		write(t, filepath.Join(dir, "export-"+id), out)
		script += id + ") cat " + dir + "/export-" + id + " ;;\n"
	}
	script += "*) printf '\\033[91mError: \\033[0mSession not found: %s\\n' \"$2\" >&2; exit 1 ;;\nesac ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "kilo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return queryFile
}

func exportJSON(id, extra string) string {
	return `{"info":{"id":"` + id + `","directory":"/p","tokens":{"input":0}},"messages":[` + extra + `]}`
}

func TestFindKiloSession(t *testing.T) {
	data := t.TempDir()
	q := fakeKilo(t, `[{"id":"`+testKiloID+`","directory":"/p","title":"t"}]`, map[string]string{testKiloID: exportJSON(testKiloID, "")})
	s, err := findKiloSession(context.Background(), data, testKiloID)
	if err != nil || s.ID != testKiloID || s.Directory != "/p" || !strings.Contains(string(s.JSON), `"messages"`) {
		t.Fatalf("got %+v, %v", s, err)
	}
	// A start of the ID works, and goes into the query without LIKE.
	if _, err := findKiloSession(context.Background(), data, "ses_e2eb4bbe"); err != nil {
		t.Errorf("prefix: %v", err)
	}
	got, _ := os.ReadFile(q)
	if strings.Contains(strings.ToLower(string(got)), "like") || !strings.Contains(string(got), "'ses_e2eb4bbe'") {
		t.Errorf("query %q", got)
	}
}

func TestFindKiloSessionNotFound(t *testing.T) {
	fakeKilo(t, `[]`, nil)
	_, err := findKiloSession(context.Background(), t.TempDir(), "ses_nothere")
	if err == nil || !strings.Contains(err.Error(), "no Kilo session ses_nothere here") {
		t.Errorf("got %v", err)
	}
}

func TestFindKiloSessionExportFails(t *testing.T) {
	// Listed, but Kilo can't export it: Kilo's own message, no escape codes.
	fakeKilo(t, `[{"id":"ses_gone","directory":"/p","title":"t"}]`, nil)
	_, err := findKiloSession(context.Background(), t.TempDir(), "ses_gone")
	if err == nil || err.Error() != "kilo export: Error: Session not found: ses_gone" {
		t.Errorf("got %v", err)
	}
}

func TestFindKiloSessionAmbiguous(t *testing.T) {
	fakeKilo(t, `[{"id":"ses_e2ebBB","directory":"/p","title":"a"},{"id":"ses_e2ebAA","directory":"/q","title":"b"}]`, nil)
	_, err := findKiloSession(context.Background(), t.TempDir(), "ses_e2eb")
	if err == nil || !strings.Contains(err.Error(), `"ses_e2eb" matches 2 Kilo sessions (ses_e2ebAA, ses_e2ebBB): give more of the ID`) {
		t.Errorf("got %v", err)
	}
}

func TestFindKiloSessionBadID(t *testing.T) {
	q := fakeKilo(t, `[]`, nil)
	for _, bad := range []string{"", "ses_", "x", "../../etc/passwd", "ses_a'; drop table session; --", "ses_a b", "*", "e62ceefd-b2d9-4e12-8261-b114765d7977"} {
		if _, err := findKiloSession(context.Background(), t.TempDir(), bad); err == nil || !strings.Contains(err.Error(), "isn't a Kilo session ID") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := os.Stat(q); err == nil {
		t.Error("a bad ID reached kilo")
	}
}

func TestFindKiloSessionNoKilo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := findKiloSession(context.Background(), t.TempDir(), testKiloID)
	if err == nil || !strings.Contains(err.Error(), "kilo isn't installed here") {
		t.Errorf("got %v", err)
	}
}

// The export that goes to the box carries no credential: not one saved in
// auth.json, and nothing beyond the session's own info and messages.
func TestKiloExportHasNoCredentials(t *testing.T) {
	const token = "sk-live-0123456789abcdef"
	data := t.TempDir()
	write(t, filepath.Join(data, "auth.json"), `{"kilo":{"type":"oauth","access":"`+token+`","refresh":"rt-9876543210zyxw"}}`)

	// A clean export passes, and what is returned is what leaves.
	fakeKilo(t, `[{"id":"`+testKiloID+`","directory":"/p","title":"t"}]`, map[string]string{testKiloID: exportJSON(testKiloID, `{"info":{"role":"user"}}`)})
	s, err := findKiloSession(context.Background(), data, testKiloID)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, "rt-9876543210zyxw"} {
		if strings.Contains(string(s.JSON), secret) {
			t.Errorf("export contains %s", secret)
		}
	}

	for name, export := range map[string]string{
		"token in a message":  exportJSON(testKiloID, `{"text":"key `+token+`"}`),
		"token in info":       strings.Replace(exportJSON(testKiloID, ""), `"directory":"/p"`, `"directory":"`+token+`"`, 1),
		"credential section":  `{"info":{"id":"` + testKiloID + `"},"messages":[],"credential":{"value":"x"}}`,
		"account section":     `{"info":{"id":"` + testKiloID + `"},"messages":[],"account":[]}`,
		"someone else's data": exportJSON("ses_other", ""),
		"not JSON":            "Exporting session",
	} {
		if _, err := checkKiloExport([]byte(export), testKiloID, []string{token}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Only the exported session goes: the script's stdin is the JSON, and
// nothing in it names Kilo's database or data directory.
func TestImportKiloScript(t *testing.T) {
	script := importKiloScript("/home/me/my app", testKiloID)
	for _, want := range []string{
		"umask 077", "mkdir -p '/run/boxctl'", "cat >'/run/boxctl/handoff-" + testKiloID + ".json'",
		`PATH="$HOME/.kilo/bin:$PATH"`, "cd '/home/me/my app' && ", "kilo import '/run/boxctl/handoff-" + testKiloID + ".json'",
		"rm -f '/run/boxctl/handoff-" + testKiloID + ".json'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	// import runs after cd, the file is removed after import.
	if strings.Index(script, "cd '") > strings.Index(script, "kilo import") || strings.Index(script, "kilo import") > strings.Index(script, "rm -f") {
		t.Errorf("wrong order:\n%s", script)
	}
	for _, bad := range []string{"kilo.db", ".local/share", "scp", "tar"} {
		if strings.Contains(script, bad) {
			t.Errorf("script mentions %q", bad)
		}
	}
}

// copyKiloSession through an ssh that runs the command locally: the
// session reaches `kilo import` as a file, from the project directory,
// and is gone afterwards -- also when the import fails.
func TestCopyKiloSession(t *testing.T) {
	bin := t.TempDir()
	proj, run, rec := t.TempDir(), t.TempDir(), t.TempDir()
	// "ssh" runs its last argument in sh, as the box would; "kilo" there
	// records where it ran and what it was given.
	write(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nfor last; do :; done\nexec sh -c \"$last\"\n")
	write(t, filepath.Join(bin, "kilo"), "#!/bin/sh\npwd >"+rec+"/cwd\ncp \"$2\" "+rec+"/got\nls -l \"$2\" >"+rec+"/ls\n[ -z \"$FAIL\" ]\n")
	for _, f := range []string{"ssh", "kilo"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	old := kiloHandoffDir
	kiloHandoffDir = run
	t.Cleanup(func() { kiloHandoffDir = old })

	s := &kiloSession{ID: testKiloID, JSON: []byte(exportJSON(testKiloID, ""))}
	box := &boxSSH{name: "kilo-x", args: []string{"root@kilo-x"}}
	if err := copyKiloSession(context.Background(), box, proj, s); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(rec, "got"))
	cwd, _ := os.ReadFile(filepath.Join(rec, "cwd"))
	ls, _ := os.ReadFile(filepath.Join(rec, "ls"))
	if string(got) != string(s.JSON) {
		t.Errorf("kilo import saw %q", got)
	}
	if want, _ := filepath.EvalSymlinks(proj); strings.TrimSpace(string(cwd)) != want {
		t.Errorf("import ran in %q, want %q", cwd, want)
	}
	if !strings.HasPrefix(string(ls), "-rw-------") {
		t.Errorf("file mode: %s", ls)
	}
	if left, _ := os.ReadDir(run); len(left) != 0 {
		t.Errorf("left on the box: %v", left)
	}

	t.Setenv("FAIL", "1")
	err := copyKiloSession(context.Background(), box, proj, s)
	if err == nil || !strings.Contains(err.Error(), "importing session "+testKiloID+" on kilo-x") {
		t.Errorf("failed import: %v", err)
	}
	if left, _ := os.ReadDir(run); len(left) != 0 {
		t.Errorf("left on the box after a failure: %v", left)
	}
}

// With a real kilo: a session made in one data directory is found,
// exported, imported into another from a clone, and listed there. The
// database file is never read. Skipped without kilo (~/.kilo/bin or PATH).
func TestKiloRoundTrip(t *testing.T) {
	kilo, err := exec.LookPath("kilo")
	if err != nil {
		if home, _ := os.UserHomeDir(); home != "" {
			kilo, err = exec.LookPath(filepath.Join(home, ".kilo", "bin", "kilo"))
		}
	}
	if err != nil {
		t.Skip("kilo isn't installed")
	}
	t.Setenv("PATH", filepath.Dir(kilo)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := t.TempDir()
	home := func(name string) {
		for _, k := range []string{"DATA", "CONFIG", "STATE", "CACHE"} {
			t.Setenv("XDG_"+k+"_HOME", filepath.Join(root, name, strings.ToLower(k)))
		}
	}
	proj := filepath.Join(root, "p1")
	run(t, root, "git", "init", "-q", proj)
	run(t, proj, "git", "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "i")

	home("local")
	// No model access here: the run stops at the sign-in error, after the
	// session exists.
	c := exec.Command(kilo, "run", "hi")
	c.Dir = proj
	_ = c.Run()
	_, data, _ := kiloDirs()
	rows, err := kiloOutput(context.Background(), "db", "select id from session", "--format", "json")
	if err != nil || !strings.Contains(string(rows), "ses_") {
		t.Skipf("no session could be made (%v): %s", err, rows)
	}
	id := rows[strings.Index(string(rows), "ses_"):]
	id = id[:strings.IndexByte(string(id), '"')]
	s, err := findKiloSession(context.Background(), data, string(id))
	if err != nil {
		t.Fatal(err)
	}

	// "The box": other data directory, a clone of the project, the script
	// the box would run (kilo from PATH here, not $HOME/.kilo/bin).
	home("box")
	clone := filepath.Join(root, "p2")
	run(t, root, "git", "clone", "-q", proj, clone)
	old := kiloHandoffDir
	kiloHandoffDir = filepath.Join(root, "box-run")
	t.Cleanup(func() { kiloHandoffDir = old })
	bin := t.TempDir()
	write(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nfor last; do :; done\nexec sh -c \"$last\"\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	box := &boxSSH{name: "box", args: []string{"root@box"}}
	for range 2 { // idempotent
		if err := copyKiloSession(context.Background(), box, clone, s); err != nil {
			t.Fatal(err)
		}
	}
	list := exec.Command("kilo", "session", "list")
	list.Dir = clone
	out, err := list.CombinedOutput()
	if err != nil || strings.Count(string(out), s.ID) != 1 {
		t.Errorf("kilo session list on the box: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "box", "data", "kilo", "kilo.db")); err != nil {
		t.Errorf("box has no database after import: %v", err)
	}
}

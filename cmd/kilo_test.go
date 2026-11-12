package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// asKilo makes Kilo the running command's agent for one test.
func asKilo(t *testing.T) {
	agent = kiloAgent
	t.Cleanup(func() { agent = claudeAgent })
}

func TestKiloSession(t *testing.T) {
	asKilo(t)
	if got := claudeBoxName("/w/My App"); got != "kilo-my-app" {
		t.Errorf("box name: %q", got)
	}
	main, auth := newTaskSession("/w/app", ""), newTaskSession("/w/app", "auth")
	if main.name != "kilo" || auth.name != "kilo-auth" || auth.dir != "/w/app@auth" {
		t.Errorf("sessions: %+v %+v", main, auth)
	}
	if got := auth.attachHint("kilo-app"); got != "boxctl kilo --box kilo-app --task auth" {
		t.Error(got)
	}
	// Kilo's positional argument is a directory: the prompt needs its flag.
	line := main.commandLine("fix it", []string{"--model", "a/b"})
	if !strings.HasSuffix(line, `PATH="$HOME/.kilo/bin:$PATH" exec kilo '--model' 'a/b' --prompt 'fix it'`) {
		t.Error(line)
	}
	if script := agent.setupScript(); !strings.Contains(script, "command -v kilo >/dev/null") || !strings.Contains(script, kiloInstall) {
		t.Error(script)
	}
}

func TestKiloSubcommands(t *testing.T) {
	for _, name := range []string{"ls", "fetch", "push"} {
		c, _, err := rootCmd.Find([]string{"kilo", name})
		if err != nil || c.Name() != name || c.Parent() != kiloCmd {
			t.Errorf("boxctl kilo %s: %v %v", name, c, err)
		}
	}
}

func TestKiloEnv(t *testing.T) {
	config, data := t.TempDir(), t.TempDir()
	write(t, filepath.Join(config, "kilo.jsonc"), `{
  // a comment: this is JSONC
  "provider": {"a": {"options": {"apiKey": "{env:BOXCTL_TEST_KEY}"}}, "b": {"options": {"apiKey": "{env:BOXCTL_TEST_UNSET}"}}}
}`)
	t.Setenv("BOXCTL_TEST_KEY", "it's secret")
	t.Setenv("BOXCTL_TEST_EXTRA", "extra")

	// No auth.json: the variables the configuration refers to, if set here.
	env, err := kiloEnv(kiloAuthAuto, config, data, nil)
	if err != nil || env != "export BOXCTL_TEST_KEY='it'\\''s secret'\n" {
		t.Fatalf("auto: %q %v", env, err)
	}
	// An auth.json without credentials is nothing to pass.
	write(t, filepath.Join(data, "auth.json"), "{}\n")
	if got, err := kiloEnv(kiloAuthAuto, config, data, nil); err != nil || got != env {
		t.Fatalf("empty auth.json: %q %v", got, err)
	}
	write(t, filepath.Join(data, "auth.json"), "{\n  \"kilo\": {\"type\": \"api\", \"key\": \"k\"}\n}\n")
	env, err = kiloEnv(kiloAuthAuto, config, data, []string{"BOXCTL_TEST_EXTRA"})
	want := "export BOXCTL_TEST_EXTRA='extra'\nexport BOXCTL_TEST_KEY='it'\\''s secret'\n" +
		`export KILO_AUTH_CONTENT='{"kilo":{"type":"api","key":"k"}}'` + "\n"
	if err != nil || env != want {
		t.Fatalf("auto with auth.json and --env:\n%s\n%v", env, err)
	}

	// login passes nothing of this machine's but what --env names.
	if env, err := kiloEnv(kiloAuthLogin, config, data, nil); err != nil || env != "" {
		t.Errorf("login: %q %v", env, err)
	}
	if env, _ := kiloEnv(kiloAuthLogin, config, data, []string{"BOXCTL_TEST_EXTRA"}); env != "export BOXCTL_TEST_EXTRA='extra'\n" {
		t.Errorf("login with --env: %q", env)
	}

	for _, bad := range []string{"BOXCTL_TEST_UNSET", "A=b", "x; rm -rf /"} {
		if _, err := kiloEnv(kiloAuthAuto, config, data, []string{bad}); err == nil {
			t.Errorf("--env %q was accepted", bad)
		}
	}
	if _, err := kiloEnv("bogus", config, data, nil); err == nil {
		t.Error("--kilo-auth bogus was accepted")
	}
	write(t, filepath.Join(data, "auth.json"), "not json")
	if _, err := kiloEnv(kiloAuthAuto, config, data, nil); err == nil {
		t.Error("a broken auth.json was passed on")
	}
}

func withHandoffFlags(t *testing.T, set func()) {
	t.Helper()
	oh, or, og, oi, ot, oo, op := claudeHandoff, claudeRepos, claudeGoal, claudeIssue, claudeTask, claudeOwnBox, claudePrompt
	t.Cleanup(func() {
		claudeHandoff, claudeRepos, claudeGoal, claudeIssue, claudeTask, claudeOwnBox, claudePrompt = oh, or, og, oi, ot, oo, op
	})
	claudeHandoff, claudeRepos, claudeGoal, claudeIssue, claudeTask, claudeOwnBox, claudePrompt = "", nil, "", 0, "", false, ""
	set()
}

func TestKiloHandoffFlagConflicts(t *testing.T) {
	cases := []struct {
		name string
		set  func()
		args []string
		want string // "" = accepted
	}{
		{"plain", func() { claudeHandoff = testKiloID }, nil, ""},
		{"args", func() { claudeHandoff = testKiloID }, []string{"--model", "a/b"}, ""},
		{"no handoff, anything goes", func() { claudeTask = "a"; claudePrompt = "x" }, []string{"--continue"}, ""},
		{"task", func() { claudeHandoff = testKiloID; claudeTask = "a" }, nil, "--task"},
		{"own-box", func() { claudeHandoff = testKiloID; claudeOwnBox = true }, nil, "--own-box"},
		{"prompt", func() { claudeHandoff = testKiloID; claudePrompt = "hi" }, nil, "--prompt"},
		{"repo", func() { claudeHandoff = testKiloID; claudeRepos = []string{"a/b"} }, nil, "--repo"},
		{"goal", func() { claudeHandoff = testKiloID; claudeGoal = "x" }, nil, "--goal"},
		{"issue", func() { claudeHandoff = testKiloID; claudeIssue = 3 }, nil, "--issue"},
		{"continue", func() { claudeHandoff = testKiloID }, []string{"--continue"}, "--continue"},
		{"-c", func() { claudeHandoff = testKiloID }, []string{"-c"}, "-c"},
		{"session=", func() { claudeHandoff = testKiloID }, []string{"--session=ses_x"}, "--session"},
		{"fork", func() { claudeHandoff = testKiloID }, []string{"--model", "m", "--fork"}, "--fork"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withHandoffFlags(t, c.set)
			err := checkKiloHandoffFlags(c.args)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("err = %v, want it to name %q", err, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), "--handoff") {
				t.Errorf("message doesn't name --handoff: %v", err)
			}
		})
	}
}

func TestKiloResumeArgs(t *testing.T) {
	got := kiloResumeArgs(testKiloID, []string{"--model", "a/b"})
	if want := []string{"--session", testKiloID, "--model", "a/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCheckNoKiloOnBox(t *testing.T) {
	bin := t.TempDir()
	write(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nfor last; do :; done\nexec sh -c \"$last\"\n")
	write(t, filepath.Join(bin, "pgrep"), "#!/bin/sh\n[ \"$1\" = -x ] && [ \"$2\" = kilo ] && [ -n \"$KILO_UP\" ]\n")
	for _, f := range []string{"ssh", "pgrep"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	box := &boxSSH{name: "kilo-x", args: []string{"root@kilo-x"}}
	if err := checkNoKiloOnBox(context.Background(), box, "kilo-x"); err != nil {
		t.Errorf("no kilo: %v", err)
	}
	t.Setenv("KILO_UP", "1")
	err := checkNoKiloOnBox(context.Background(), box, "kilo-x")
	if err == nil || !strings.Contains(err.Error(), "another Kilo is running on kilo-x") {
		t.Errorf("kilo running: %v", err)
	}
}

func TestKiloHandoffFlagHelp(t *testing.T) {
	f := kiloCmd.Flags().Lookup("handoff")
	if f == nil || !strings.Contains(f.Usage, "Kilo session") {
		t.Fatalf("handoff flag: %+v", f)
	}
}

// An unknown session fails in runKilo before openSession: no API call is
// made (config has no token and there is no server), and the error is the
// lookup's.
func TestRunKiloHandoffValidatesFirst(t *testing.T) {
	asKilo(t)
	fakeKilo(t, `[]`, nil)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	withHandoffFlags(t, func() { claudeHandoff = "ses_nope" })
	cmd := kiloCmd
	cmd.SetContext(context.Background())
	err := runKilo(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no Kilo session ses_nope here") {
		t.Fatalf("err = %v", err)
	}
}

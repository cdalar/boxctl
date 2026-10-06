package cmd

import (
	"path/filepath"
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
	if main.tmux != "kilo" || auth.tmux != "kilo-auth" || auth.dir != "/w/app@auth" {
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

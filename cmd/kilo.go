package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// `boxctl kilo`: the Kilo CLI on a box, the way `boxctl claude` runs
// Claude Code there -- the same box, project copy, tmux, tasks, GitHub
// and fetch/push (see claude.go and agent.go). What's Kilo's own is here:
// its installer, its configuration (~/.config/kilo) and its credentials.

const (
	// kiloInstall is Kilo's installer, run on first use like Claude's. It
	// installs to ~/.kilo/bin; boxctl puts that on the session's PATH, so
	// the box's shell configuration is left alone.
	kiloInstall = "curl -fsSL https://kilo.ai/cli/install | bash -s -- --no-modify-path"

	kiloAuthAuto  = "auto"  // this machine's Kilo credentials, in the session's environment
	kiloAuthLogin = "login" // none: log in on the box
)

var kiloAgent = &boxAgent{
	name: "kilo", title: "Kilo", product: "Kilo",
	bin: "kilo", binDir: ".kilo/bin", install: kiloInstall,
	promptFlag: "--prompt",
}

var (
	kiloAuth     string
	kiloEnvNames []string
)

var kiloCmd = &cobra.Command{
	Use:   "kilo [-- kilo-args...]",
	Short: "Run Kilo on a box, with this project on it",
	Long: `Runs the Kilo CLI on a box instead of on this machine, attached to your
terminal -- boxctl claude, for Kilo. The first time, it creates the
project's box (kilo-<project>), copies the project there -- the same
absolute path, .git and uncommitted changes included, .gitignore'd files
left out -- installs Kilo and your Kilo configuration, and starts Kilo
in it.

Kilo runs in tmux, so it keeps going when you detach (Ctrl-b d) or the
connection drops; run boxctl kilo again to come back to it. The box's
copy of the project is the one Kilo works on: later runs don't copy it
again, and nothing is synced back -- Kilo commits and pushes from the
box, or boxctl kilo fetch brings its work here.

  boxctl kilo                           # create or reattach
  boxctl kilo --prompt "fix the flaky auth test"
  boxctl kilo --detach --prompt "..."   # start it, don't attach
  boxctl kilo -- --model provider/model # arguments for kilo itself
  boxctl kilo --task auth               # a second Kilo, on its own branch
  boxctl kilo --env OPENAI_API_KEY      # a provider key from this shell

Kilo on the box is logged in as it is here: the credentials kilo auth
login saved on this machine, and the environment variables your Kilo
configuration refers to ({env:NAME}), go into the session's environment
when it starts -- never onto the box's disk. --env NAME passes one more
variable the same way. --kilo-auth login passes none of yours: log in on
the box instead (/connect), and that login stays there.

Your configuration (~/.config/kilo: kilo.json(c), AGENTS.md, agents,
commands, modes, skills) is copied as it is, so a provider it points at
on this machine or your own network isn't reachable from the box.

GitHub (--github), tasks (--task, --own-box) and the idle TTL work as
they do for boxctl claude.`,
	RunE: kiloAgent.runs(runKilo),
}

func init() {
	kiloAgent.addFlags(kiloCmd)
	kiloCmd.Flags().StringVar(&kiloAuth, "kilo-auth", kiloAuthAuto, "how Kilo on the box logs in: auto (this machine's Kilo credentials, passed in its environment) or login (on the box)")
	_ = kiloCmd.RegisterFlagCompletionFunc("kilo-auth", cobra.FixedCompletions([]string{kiloAuthAuto, kiloAuthLogin}, cobra.ShellCompDirectiveNoFileComp))
	kiloCmd.Flags().StringArrayVarP(&kiloEnvNames, "env", "e", nil, "also pass this environment variable from here to a newly started Kilo (repeatable)")
	kiloCmd.AddCommand(kiloAgent.lsCmd(), kiloAgent.fetchCmd(), kiloAgent.pushCmd())
	rootCmd.AddCommand(kiloCmd)
}

func runKilo(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if err := checkSessionFlags(); err != nil {
		return err
	}
	configDir, dataDir, err := kiloDirs()
	if err != nil {
		return err
	}
	// Before any box is created or touched, like Claude's credentials.
	env, err := kiloEnv(kiloAuth, configDir, dataDir, kiloEnvNames)
	if err != nil {
		return err
	}

	t, sess, err := openSession(ctx, cmd.Flags().Changed("idle-ttl"))
	if err != nil {
		return err
	}
	defer t.box.close()

	if err := copyKiloConfig(ctx, t.box, configDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: couldn't copy your Kilo configuration: %v\n", err)
	}
	if err := t.box.run(ctx, agent.setupScript(), nil); err != nil {
		return fmt.Errorf("installing Kilo on %s: %w", t.name, err)
	}
	running := sessionRunning(ctx, t.box, sess)
	if !running {
		if env == "" {
			fmt.Fprintln(os.Stderr, "Kilo on the box has no credentials from here: log it in there with /connect.")
		}
		if err := stageClaudeEnv(ctx, t.box, sess, env); err != nil {
			return fmt.Errorf("passing Kilo its credentials: %w", err)
		}
	}
	return startSession(ctx, t, sess, running, args)
}

// kiloDirs are Kilo's configuration and data directories here. It
// follows XDG on every platform, macOS included.
func kiloDirs() (config, data string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	config, data = os.Getenv("XDG_CONFIG_HOME"), os.Getenv("XDG_DATA_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(config, "kilo"), filepath.Join(data, "kilo"), nil
}

// kiloConfigFiles are Kilo's user-level configuration files, and
// kiloConfigEntries everything of ~/.config/kilo that goes to the box:
// those plus what shapes how Kilo works. Not plugin/ or node_modules,
// which are this machine's.
var (
	kiloConfigFiles   = []string{"kilo.json", "kilo.jsonc", "opencode.json", "opencode.jsonc"}
	kiloConfigEntries = append([]string{"AGENTS.md", "agent", "agents", "command", "commands", "mode", "modes", "skill", "skills"}, kiloConfigFiles...)
)

// copyKiloConfig copies the user-level Kilo configuration to the box's
// ~/.config/kilo, every time, so the box follows edits made here. The
// files go as they are: Kilo's configuration is JSON with comments, and
// what it points at (providers, MCP servers) is Kilo's to report when it
// can't reach it.
func copyKiloConfig(ctx context.Context, box *boxSSH, src string) error {
	var files []string
	for _, entry := range kiloConfigEntries {
		sub, err := walkFiles(src, entry)
		if err != nil {
			return err
		}
		files = append(files, sub...)
	}
	if len(files) == 0 {
		return nil
	}
	return streamTar(ctx, box, "mkdir -p ~/.config/kilo && tar -xzf - -C ~/.config/kilo",
		func(tw *tar.Writer) error { return addFiles(tw, src, files) })
}

var (
	// kiloEnvRef is how Kilo's configuration refers to an environment
	// variable: "apiKey": "{env:NAME}".
	kiloEnvRef     = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// kiloEnv is what a new Kilo session gets in its environment, as a
// sourceable file ("" for nothing): in auto mode the credentials kilo
// auth login saved here (auth.json, which Kilo also reads from
// KILO_AUTH_CONTENT) and the variables the configuration refers to that
// are set here; in either mode, the ones named with --env.
func kiloEnv(mode, configDir, dataDir string, names []string) (string, error) {
	vars := map[string]string{}
	for _, name := range names {
		if !envNamePattern.MatchString(name) {
			return "", fmt.Errorf("--env %q isn't an environment variable's name", name)
		}
		v, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("--env %s: not set here", name)
		}
		vars[name] = v
	}
	switch mode {
	case kiloAuthLogin:
	case kiloAuthAuto:
		for _, f := range kiloConfigFiles {
			data, err := os.ReadFile(filepath.Join(configDir, f))
			if err != nil {
				continue
			}
			for _, m := range kiloEnvRef.FindAllSubmatch(data, -1) {
				if v, ok := os.LookupEnv(string(m[1])); ok {
					vars[string(m[1])] = v
				}
			}
		}
		auth, err := kiloAuthContent(filepath.Join(dataDir, "auth.json"))
		if err != nil {
			return "", err
		}
		if auth != "" {
			vars["KILO_AUTH_CONTENT"] = auth
		}
	default:
		return "", fmt.Errorf("--kilo-auth must be %s or %s", kiloAuthAuto, kiloAuthLogin)
	}
	sorted := make([]string, 0, len(vars))
	for name := range vars {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	var env strings.Builder
	for _, name := range sorted {
		env.WriteString("export " + name + "=" + shellQuote(vars[name]) + "\n")
	}
	return env.String(), nil
}

// kiloAuthContent is auth.json on one line, or "" when there's none or it
// holds no credentials.
func kiloAuthContent(p string) (string, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var creds map[string]json.RawMessage
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if len(creds) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

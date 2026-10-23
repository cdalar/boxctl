package cmd

import (
	"strings"

	"github.com/spf13/cobra"
)

// boxAgent is a coding agent boxctl runs on a box -- `boxctl claude`,
// `boxctl kilo` -- or rather the little that differs between them: what
// it's called, and how it's installed and started. The box, the project
// copy, the multiplexer, tasks, GitHub and fetch/push are the same for all of them
// (the claude*.go files), and read the running command's agent from
// `agent`. So are the flags: each agent's command binds the same
// variables (claudeBox, claudeTask, ...), since only one of them runs.
type boxAgent struct {
	name    string // the subcommand, the box-name prefix and the session's name
	title   string // what messages call it
	product string // what it's installed as
	bin     string // its command on the box
	binDir  string // where its installer puts that, under $HOME
	install string // its installer, run on the box at first use
	// promptFlag is the flag a new session's first message goes after;
	// "" when it's a plain argument.
	promptFlag string
}

var claudeAgent = &boxAgent{
	name: "claude", title: "Claude", product: "Claude Code",
	bin: "claude", binDir: ".local/bin", install: claudeInstall,
}

// agent is the one the running command is for.
var agent = claudeAgent

// fill words text for this agent: {agent} is its subcommand, {Agent}
// what it's called.
func (a *boxAgent) fill(text string) string {
	return strings.NewReplacer("{agent}", a.name, "{Agent}", a.title).Replace(text)
}

// runs is run as a RunE of one of this agent's commands: it makes the
// agent, and --mux's multiplexer, the running command's.
func (a *boxAgent) runs(run func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		agent = a
		m, err := muxNamed(muxArg)
		if err != nil {
			return err
		}
		mux, muxExplicit = m, cmd.Flags().Changed("mux")
		return run(cmd, args)
	}
}

// setupScript makes sure the box can run the agent in the multiplexer.
// The claude-agent image already has tmux and Claude Code, so this does
// nothing there; other images get the multiplexer (mux.setup) and the
// agent's own install, once.
func (a *boxAgent) setupScript() string {
	return `set -e
export PATH="$HOME/` + a.binDir + `:$PATH"
` + mux.setup() + `
command -v ` + a.bin + ` >/dev/null || { echo "Installing ` + a.product + `..." >&2; ` + a.install + ` >&2; }`
}

// addFlags registers what every agent's command takes: the persistent
// flags its subcommands (ls, fetch, push) share, and the ones for
// starting a session.
func (a *boxAgent) addFlags(c *cobra.Command) {
	c.PersistentFlags().StringVar(&claudeBox, "box", "", a.fill("box to use, kept after the run (default: {agent}-<project directory name> with --keep and for ls/fetch/push; otherwise a new box of the run's own)"))
	c.PersistentFlags().StringVar(&claudeProject, "project", "", "project directory (default: the git repository, or directory, you're in)")
	c.PersistentFlags().StringVarP(&claudeTask, "task", "t", "", a.fill("work on a task in parallel: its own worktree (<dir>@<task>, branch <task>) and {Agent} on the box"))
	c.PersistentFlags().BoolVar(&claudeOwnBox, "own-box", false, a.fill("with --task: give the task a box of its own ({agent}-<project>-<task>) instead of a worktree"))
	c.Flags().StringVarP(&claudeSize, "size", "s", claudeDefaultSize, "size of a box created for this")
	_ = c.RegisterFlagCompletionFunc("size", completeSize)
	c.Flags().StringVarP(&claudeImage, "image", "i", "", "image of a box created for this (default: one for the project's language if there is one, else "+claudeDefaultImage+")")
	c.Flags().StringVarP(&claudePrompt, "prompt", "p", "", a.fill("first message for a newly started {Agent}"))
	c.Flags().BoolVarP(&claudeDetach, "detach", "d", false, a.fill("start {Agent} on the box without attaching to it"))
	c.Flags().StringVar(&claudeGitHub, "github", githubStore, "GitHub credentials for the box: store (BOXCTL_GITHUB_TOKEN, else your gh token, kept on the box) or off")
	_ = c.RegisterFlagCompletionFunc("github", cobra.FixedCompletions([]string{githubStore, githubOff}, cobra.ShellCompDirectiveNoFileComp))
	c.Flags().BoolVarP(&claudeKeep, "keep", "k", false, a.fill("use the project's own box, {agent}-<project>, and keep it -- instead of a new box deleted when the run ends (--box, --detach and --task keep it too)"))
	c.Flags().BoolVarP(&claudeForce, "force", "f", false, "copy a project directory even if it isn't a git repository")
	c.Flags().DurationVar(&claudeIdle, "idle-ttl", claudeDefaultIdleTTL, "pause the box after this long unused, 10m to 720h (set on a new box, or on an existing one when given)")
	c.Flags().StringVar(&muxArg, "mux", defaultMux(), a.fill("what keeps {Agent} running on the box between attaches: tmux, or herdr (herdr.dev, installed on the box the first time). "+muxEnv+" sets the default; a box keeps the one its sessions started in"))
	_ = c.RegisterFlagCompletionFunc("mux", cobra.FixedCompletions([]string{"tmux", "herdr"}, cobra.ShellCompDirectiveNoFileComp))
}

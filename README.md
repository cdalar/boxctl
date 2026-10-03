# boxctl

`boxctl` is the command-line client for [boxctl.io](https://boxctl.io) —
boot, list, connect to, and destroy your Firecracker microVMs ("boxes")
without opening the dashboard. It talks to the same `boxctl-vms` control
plane the web dashboard does, authenticating as you with a personal API
token instead of a browser session.

It's a client only: all VM lifecycle logic (dispatching `onctl`, tracking
state, the terminal tunnel) lives in `boxctl-vms` (separate repo). See
that repo's `AGENTS.md`/`README.md` for the backend architecture, and
`boxctl-web`'s `AGENTS.md` for the dashboard this mirrors.

## Install

Linux and macOS (install script, latest tagged release):

```bash
curl -sLS https://boxctl.io/get.sh | bash
sudo install boxctl /usr/local/bin/
```

Windows: download the binary from the [releases page](https://github.com/cdalar/boxctl/releases).

macOS: the same install script works. The macOS binaries aren't signed or
notarized yet, which doesn't matter when installing with `curl` as above; a
binary downloaded through a browser gets quarantined, so clear it with
`xattr -d com.apple.quarantine boxctl`.

#### Edge build (latest `main`, Linux and macOS)

To install or update to the `edge` build, an unsigned binary rebuilt from the tip of `main` on every push (no Windows build):

```bash
curl -sLS https://boxctl.io/get-edge.sh | bash
sudo install boxctl /usr/local/bin/
```

Or build from source:

```bash
go build -o boxctl .
```

#### Shell completion

`get.sh` and `get-edge.sh` also install tab completion for your login
shell (bash, zsh or fish) -- subcommands, flags, and your own box names
for `ssh`/`port-forward`/`rm`/`pause`/`resume`/`download`, fetched live from the API
(`boxctl resume mig<TAB>`). Set `BOXCTL_NO_COMPLETION=1` to skip that.
To set it up by hand instead, e.g. after building from source:

```bash
echo 'source <(boxctl completion bash)' >> ~/.bashrc   # bash
echo 'source <(boxctl completion zsh)' >> ~/.zshrc     # zsh (after compinit)
boxctl completion fish > ~/.config/fish/completions/boxctl.fish
```

## Releasing

Push a `vX.Y.Z` tag and `.github/workflows/release.yml` does the rest via
GoReleaser (`.goreleaser.yml`, mirroring onctl's) on the `onctl-4` runner:
Linux (amd64/arm64), macOS (amd64/arm64, unsigned) and Windows (amd64)
binaries are built, `checksums.txt` is GPG-signed, and the GitHub release
is created. The signed macOS build (quill signing/notarization) and the
Homebrew cask are kept commented out in both files until this repo has
the Apple credentials and a Mac runner. Run the "release (self-hosted test)" workflow against
an existing tag to dry-run all of that without publishing.
`make release-snapshot` builds the same artifacts locally (unsigned,
into `dist/`).

## Usage

```bash
# Create a personal token at https://boxctl.io/dashboard/tokens, then paste it
# at the hidden prompt (or pipe it in: `pbpaste | boxctl login`).
boxctl login

boxctl ls
boxctl ls -o json                 # for scripts; images and sizes take -o json too
boxctl images
boxctl create my-box
boxctl sizes                      # vCPU, memory and disk of small (default), medium, large
boxctl create big-box --size large
boxctl ssh my-box
boxctl ssh my-box -- ls -al   # run one command instead of a shell
boxctl port-forward my-box 3000   # localhost:3000 -> port 3000 inside the box
ssh -o ProxyCommand='boxctl ssh-proxy %h' root@my-box   # real ssh (key needed, see below)
boxctl claude                     # Claude Code on a box, with this project (see below)
boxctl pause my-box
boxctl resume my-box
boxctl rm my-box

boxctl logout   # forgets the token locally; revoke it from the dashboard too
```

Every command talks to `https://vms-backend.boxctl.io` by default; override
with `--api-url` (or by passing a different one to `boxctl login`) for a
local/dev `boxctl-vms` instance.

## Claude Code on a box (`boxctl claude`)

Runs Claude Code itself on a box instead of on your machine, attached to
your terminal -- so its file edits and its shell both happen there, on
the box's copy of the project, and nothing is synced.

```bash
cd ~/src/myproject
boxctl claude                           # create or reattach to claude-myproject
boxctl claude --prompt "fix the flaky auth test"
boxctl claude --detach --prompt "..."   # start it without attaching
boxctl claude -- --model opus           # arguments for claude itself
```

The first run creates the project's box (`claude-<directory>`, from the
`claude-agent` image, `medium`), copies the project to the **same
absolute path** -- what git counts as the project (`git ls-files
--cached --others --exclude-standard`, so uncommitted changes and
untracked files go, `.gitignore`'d ones don't) plus `.git` -- installs
Claude Code with Anthropic's installer, copies your `~/.claude/CLAUDE.md`,
`agents/`, `skills/`, `commands/` and `settings.json` (minus hooks, the
status line, plugins, `env` and credential helpers, which point at this
machine), and starts `claude` in tmux. Log it in once with `/login`: it
prints a URL to open here and a code to paste back, and the login stays
on the box.

**GitHub.** While you're attached, Claude on the box can `git push` and
`gh pr create`/`merge` as you: the box asks this machine for your `gh`
token each time it needs one, over the ssh connection, and keeps nothing
(`--github forward`, the default). Each use is logged to
`~/.boxctl/claude/github.log`, and detaching prints a count. Detached,
the box has no GitHub access. For work that has to reach GitHub while
you're away, `--github store` keeps a token on the box until it's
destroyed -- it asks you for one (or reads `BOXCTL_GITHUB_TOKEN`) rather
than using your gh token, so make it a fine-grained token for just that
repository. `--github off` gives the box nothing. Your git `user.name`
and `user.email` are set on the box either way, so its commits are yours.
Forwarding needs the `claude-agent` image's credential helper and `gh`
wrapper (boxctl-vms `images/claude-agent/`).

Claude keeps running when you detach (`Ctrl-b d`) or the connection
drops; `boxctl claude` again attaches to it. Later runs don't copy the
project again -- the box's copy is the one Claude works on, so get its
work back the git way (Claude commits and pushes from the box). Plan and
what's next (fetching the box's branches, handing off a running
session): boxctl-vms's `docs/plans/claude-on-the-box.md`.

It all runs over real ssh to the box's sshd, as root, with the key in
`~/.boxctl/claude/id_ed25519` (authorized on the box over `boxctl ssh`),
tunneled by `boxctl ssh-proxy`.

## Real ssh (`ssh-proxy`)

`boxctl ssh` runs a shell or one command over boxctl's own terminal
protocol. For everything else ssh does -- `scp`, `rsync`, `git` over ssh,
port and socket forwarding, VS Code Remote-SSH -- `boxctl ssh-proxy <box>
[port]` connects its stdin and stdout to the box's sshd (or another port)
through the same tunnel as `port-forward`, for use as ssh's
`ProxyCommand`. A paused box is resumed first.

```
# ~/.ssh/config
Host *.box
  ProxyCommand boxctl ssh-proxy %n
  User root
```

then `ssh my-box.box`, `rsync -a dir/ my-box.box:/srv/`. Boxes accept
key logins for root: add your public key once with
`boxctl ssh my-box -- "mkdir -p ~/.ssh && echo '<your key>' >> ~/.ssh/authorized_keys"`.

## Claude Code plugin

`claude-plugin/` is a Claude Code plugin that runs Claude's Bash commands in
one of your boxes instead of on your machine. Install it from this repo:

```
/plugin marketplace add cdalar/boxctl
/plugin install boxctl@boxctl
```

It needs `boxctl` (logged in) and `jq` on your `PATH`. Then:

```
/boxctl:on [--mode session|project|exec] [--workspace sync|copy|git|none] [box-name] [--size medium] [--image name]
/boxctl:status
/boxctl:off
```

| `--mode` | Box | `/boxctl:off` |
|---|---|---|
| `session` (default) | One box for this Claude Code session, destroyed when the session ends (a `SessionEnd` hook) | destroys it |
| `project` | One box for this project directory, reused by every session there | leaves it running |
| `exec` | A fresh, disposable box for every command (`boxctl exec`); nothing persists | -- |

`--workspace` is how the project gets onto a session or project box,
always at the same absolute path it has locally:

| `--workspace` | The box has |
|---|---|
| `sync` (default) | The project, copied there before every command and back after, so Claude's Read/Edit/Write and the box's Bash always see the same files (about a second per command) |
| `copy` | The project as it was when the box was turned on |
| `git` | A clone of `origin` at the local commit. A GitHub origin clones over https with the local `gh auth token` (passed on stdin, deleted afterwards); anything else clones with the local ssh agent forwarded |
| `none` | An empty directory |

`.gitignore`'d files are never copied in either direction, so
`node_modules`, build output and `.env` stay on whichever side made them.
The copying is `rsync` over `ssh` through a background
`boxctl port-forward <box> :22`, with a key the plugin generates
(`~/.boxctl/claude/id_ed25519`) and authorizes on the box over
`boxctl ssh`; `rsync`/`git` are installed on the box if missing.

Boxes boot the `claude-agent` image unless the **Boot image** option or
`--image` says otherwise: `debian-slim` plus git, openssh-client, rsync,
jq, ripgrep and make, so neither a workspace nor Claude's first command
starts with an `apt-get install`.

To skip `/boxctl:on`, set the plugin's **Auto-on at session start**
option (`autostart` in `/config`, or when the plugin is enabled) to a
mode: every new session then turns remote bash on by itself, using the
plugin's **Workspace**, **Box size** and **Boot image** options (which
`/boxctl:on` uses too, unless its own flags override them),
and tells Claude where its Bash runs. It defaults to `off`. If the box
can't be had (not logged in, API down), the session starts with local
bash and a message saying why. Resumed sessions keep what they have, and
`/boxctl:off` still works for the rest of a session.

A session's own routing wins over its project's, so `/boxctl:on` in one
session doesn't touch others in the same project unless you ask for
`--mode project`.

A `PreToolUse` hook (`claude-plugin/hooks/route-bash`) rewrites each Bash
command to `boxctl-claude run` (or `exec`), which sends it base64-encoded
through `boxctl ssh <box> --` (or `boxctl exec`) and relays stdout, stderr
and the exit code. On a session or project box the working directory
carries over between commands (kept on the box), the box starts in the
same absolute path as the local project, and a box the idle reaper paused
is resumed on the next command. Each command is capped at 5 minutes and has no stdin, like `boxctl ssh --`. Commands that
start with a `# local` line, or with `boxctl `, run locally. Routing is
kept in `~/.boxctl/claude/` (`sessions/<id>`, `projects/<hash>`). If
Claude Code is killed rather than exited, `SessionEnd` never runs and a
session box is left behind; the idle reaper pauses it, and `boxctl ls`
shows it as `claude-<session id prefix>`.

## How auth works

A personal token is scoped to your own boxes only — `boxctl-vms` resolves
it to your owner prefix and enforces that scoping itself (see its
README's "Personal API tokens (the `boxctl` CLI)"), the same way
`boxctl-web`'s dashboard already scopes itself to your boxes with its own
shared token. Your token lives in `~/.boxctl/config.json` (mode `0600`)
and is never sent anywhere except the configured API URL.

## Project layout

```
main.go                 Entry point, delegates to cmd.Execute()
cmd/                     Cobra subcommands (login/logout/ls/images/sizes/create/rm/pause/resume/ssh/port-forward/version),
                         plus box-name tab completion (complete.go)
internal/client/         HTTP client for boxctl-vms's /api/vms* and /api/images routes
internal/config/         ~/.boxctl/config.json read/write
claude-plugin/           Claude Code plugin: route Bash to a box (see "Claude Code plugin")
.claude-plugin/          Marketplace manifest listing that plugin
```

## `ssh` / terminal protocol

`boxctl ssh <name>` mints a short-lived, single-use ticket
(`POST /api/vms/{name}/terminal-ticket`) and opens
`wss://.../ws/terminal/{vm_id}?ticket=...` directly — the same WebSocket
endpoint the browser dashboard's xterm.js terminal uses. Resize events are
sent as a `0x01`-prefixed JSON payload on the same binary stream
(`internal/ptyrelay.ResizeMarker` in `boxctl-vms`); this CLI polls the
local terminal size once a second rather than hooking `SIGWINCH`, since
that signal doesn't exist on Windows.

`boxctl ssh <name> -- command [args...]` skips the WebSocket entirely: the
words after `--` are joined with spaces (like `ssh host -- cmd`) and sent
to `POST /api/vms/{name}/exec`, the same no-pty endpoint `boxctl exec`
uses. The command's stdout and stderr are relayed to yours and the process
exits with the command's own exit code; `-T/--timeout` (default 30s, max
5m) bounds how long it may run.

## `port-forward`

`boxctl port-forward <name> [LOCAL:]REMOTE [...]` listens on
`127.0.0.1:LOCAL` (`--address` to change; `:REMOTE` picks a free local
port) and, for **each** accepted connection, opens
`wss://.../api/vms/{name}/port/{REMOTE}` with the personal token on the
upgrade request -- no ticket, since a browser page load opens dozens of
connections and a ticket per socket would double the round trips.
`boxctl-vms` has the box's host agent dial the port and relays raw bytes
in binary WebSocket messages both ways. Each new connection pays that
rendezvous (a few round trips to the controller), so it's for your own
traffic, not for serving the public. If nothing in the box is listening,
the far end closes with the dial error as its reason and this prints it
(`my-box:3000: dial tcp ...: connection refused`) -- the connection is
dropped, the forward keeps running. Traffic through a forward counts as
using the box for idle auto-pause.

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

boxctl ls                         # running boxes
boxctl ls -a                      # all of them, paused and stopped too
boxctl ls -o json                 # for scripts; images and sizes take -o json too
boxctl images
boxctl create my-box
boxctl create                     # a generated name, like quiet-otter-1a2b
# an account has at most 2 active (running or paused) boxes; over that, create says which ones you have
boxctl sizes                      # vCPU, memory and disk of small (default), ai-sandbox, medium, large
boxctl create big-box --size large
boxctl create long-job --idle-ttl 48h   # pause after 48h unused (default 1h)
boxctl create k3s -a k3s/k3s-server.sh  # run an onctl-templates script on the new box, streaming its log
boxctl ssh my-box
boxctl ssh my-box -- ls -al   # run one command instead of a shell
boxctl port-forward my-box 3000   # localhost:3000 -> port 3000 inside the box
boxctl expose my-box 3000         # a public https://my-box-xxxxxxxx.boxctl.app -> port 3000
boxctl unexpose my-box 3000       # take it down again
ssh -o ProxyCommand='boxctl ssh-proxy %h' root@my-box   # real ssh (key needed, see below)
boxctl claude                     # Claude Code on a box, with this project (see below)
boxctl kilo                       # the same, for the Kilo CLI (see below)
boxctl pause my-box
boxctl resume my-box
boxctl keep my-box                # don't let the idle reaper delete it for a day (below)
boxctl label my-box team=web old- # show, set or remove a box's labels
boxctl backups                    # saved copies of paused boxes (made by `boxctl download` or the dashboard)
boxctl restore my-box             # newest backup of my-box, back as a paused box
boxctl restore my-box -n big-box --size large   # ...or booted fresh at another size (files kept, running programs not)
boxctl exec 'uname -a'            # one command in a fresh, disposable box; prints JSON (--image, --size, --timeout)
boxctl download my-box            # a paused box's snapshot bundle -> my-box.tar.zst
boxctl import my-box.tar.zst      # ...and back as a new, paused box (-n to name it)
boxctl rm my-box                  # several at once: boxctl rm a b c
boxctl pause all                  # also resume all / rm all: every box it applies to (rm asks first; --force skips)

boxctl logout   # forgets the token locally; revoke it from the dashboard too
```

Every command talks to `https://vms-backend.boxctl.io` by default; override
with `--api-url` (or by passing a different one to `boxctl login`) for a
local/dev `boxctl-vms` instance.

**Unused boxes are paused, then deleted.** A running box nobody has used
for its idle TTL (1 hour unless you set `--idle-ttl`) is paused, which
loses nothing: `boxctl resume` brings it back exactly as it was. A box
the server paused this way and that stays paused for 24 hours is backed
up and then deleted from the host -- only once the backup is safely
uploaded, and `boxctl restore <box>` brings it back. `boxctl keep <box>` holds that off for 24 hours from when
you run it (running it again restarts the 24 hours rather than adding to
them), and resuming the box stops it altogether. A box you paused
yourself with `boxctl pause` is never deleted.

## Claude Code on a box (`boxctl claude`)

Runs Claude Code itself on a box instead of on your machine, attached to
your terminal -- so its file edits and its shell both happen there, on
the box's copy of the project, and nothing is synced.

A box created for Claude (or Kilo) is the `ai-sandbox` size, 2 vCPU / 4 GiB RAM with a 10 GiB disk unless you pass `--size`.

By default every run gets a fresh box (`<directory>-<slug>-<id>`)
that lives only as long as the run. A git repository with an `origin`
isn't copied: the box clones origin's default branch (your GitHub token
lent for it, as below) and Claude works on a new branch,
`boxctl/<slug>-<id>` -- so local uncommitted or unpushed work
isn't there; push it first. Anything else is copied. When the run ends --
Claude exits, you detach, or the connection drops -- the box's branches and uncommitted work are
fetched here as `box/*` (what `boxctl claude fetch` does) and the box is
deleted -- or kept, with a message, if that fetch fails. Ctrl-C during
setup deletes it too. `--keep` uses the project's long-lived box
(`claude-<directory>`) instead, as described below; `--box`, `--detach`
and `--task` imply it.

A box you don't name gets a generated one, `<repository>-<slug>-<id>`
(at most 40 characters): the slug is two or three words of `--goal` or
`--prompt`, or an adjective-noun pair like `quiet-otter`, the id four
random characters, and the branch Claude works on is `boxctl/<slug>-<id>`,
so you can tell which box a branch came from; `--box` names it yourself.

```bash
cd ~/src/myproject
boxctl claude                           # a new box for this run, deleted afterwards
boxctl claude --keep                    # create or reattach to claude-myproject
boxctl claude --prompt "fix the flaky auth test"
boxctl claude --detach --prompt "..."   # start it without attaching
boxctl claude -- --model opus           # arguments for claude itself
boxctl claude login                     # once: every box's Claude is logged in (below)
boxctl claude --task auth               # a second Claude, in parallel, on its own branch
boxctl claude ls                        # this project's Claude sessions
boxctl claude --handoff <session-id>    # carry on a local session on the box
boxctl claude fetch                     # the box's branches and uncommitted work, as box/*
boxctl claude push                      # this branch to the box
boxctl claude -R owner/name              # --repo for short: a fresh box with the repo cloned, on a new branch
boxctl claude --repo owner/name --goal "bump Go to 1.24 and fix what breaks" --github store
boxctl claude --repo owner/api --repo owner/web --goal "rename the /v1 route"  --github store   # several repos, a PR in each
boxctl claude logs <box> [-f]           # how a --goal run is going
boxctl claude --harness pr-only         # extra rules for the box: CLAUDE.md, managed-settings.json (below)
```

**A repository instead of a local copy.** `--repo owner/name` creates a
new box, clones the repository's default branch from GitHub onto a new
branch `boxctl/<slug>-<id>`, and starts Claude there, attached as usual
(come back with `boxctl ssh <box>`, then `tmux attach -t claude`). With
`--goal "..."` Claude doesn't wait for you: it runs `claude -p` to the end
in a detached tmux session, then opens a pull request with `gh pr create`
whose body has the goal and a summary. `boxctl claude` prints the box, the
branch and the `boxctl claude logs <box>` command and returns; closing
the terminal changes nothing. Without `--repo`, `--goal` uses the git
repository of the directory you run it in (its GitHub `origin`). The run's
status (`running`, `done` or `failed`, with the PR's URL) is kept on the
box, in `/root/.boxctl/goals/latest/status.json`, and `logs` prints it
before the log. Claude must be logged in without you (`boxctl claude
login`, or `--claude-auth api-key`).

**Several repositories.** Repeat `--repo` (or comma-separate: `--repo a/api,a/web`) to
work across repositories in one box. They are cloned side by side under
`/root/workspace`, each on the same new branch, and Claude starts in that
directory. With `--goal`, Claude opens one pull request in each repository
it changed, linking them to each other; `status.json` has the first as
`pr_url` and all of them in `pr_urls`. GitHub only; private repositories
work too, so give `--github store` a token that covers every one. The
repositories need different names, and `--repo` cannot be combined with
`--project`.

**A harness: rules for Claude on the box.** Your own `~/.claude` is copied
to the box, but it also steers the Claude on this machine. `--harness
<dir>` (or a bare name, looked up in `~/.boxctl/harness/<name>`) adds
rules for box runs only. The directory may hold:

| Entry | Goes to | Effect |
| --- | --- | --- |
| `CLAUDE.md` | `/etc/claude-code/CLAUDE.md` | managed memory, loaded in every session and not excludable |
| `managed-settings.json` | `/etc/claude-code/managed-settings.json` | highest-precedence settings; user and project settings can't undo its permission rules |
| `agents/`, `skills/`, `commands/` | `~/.claude`, on top of yours | e.g. an `/open-pr` command |

```json
{"permissions": {"deny": ["Bash(gh pr merge:*)", "Bash(git push --force*)", "Bash(git push*main*)"]},
 "allowManagedPermissionRulesOnly": true}
```

(a ready-made one: `cmd/harness-pr-only`) with a `CLAUDE.md` saying "this is a disposable sandbox: read any
directory, install any tool; work on your branch, open a pull request with
`gh pr create`, never merge it" gives `boxctl claude --harness pr-only` a
"PR, never merge" run. The harness is validated before any box is created
and a harness that fails to arrive stops the run rather than starting
Claude without it. Two limits: Claude runs as root on the box, so
managed settings are a guardrail against mistakes, not against a
determined agent -- the real boundary is the credentials (`--github store`
with a one-repository fine-grained token, and branch protection on
`main` so nothing merges without review). And whether deny rules hold
under `--goal`'s `--dangerously-skip-permissions` is unverified against a
real box.

**Which harness a run gets.** `--harness none` means none; `--harness
<dir|name>` means that one; otherwise `~/.boxctl/harness/default`, if it
exists, applies to every run; otherwise a `--goal` run gets the built-in
`pr-only` (pull request, never merge) -- it runs unattended with
`--dangerously-skip-permissions`, so it's the run that most needs a
guardrail you didn't have to ask for. Anything chosen implicitly is
announced on stderr. An attended run with no default directory gets no
harness.

**`--github` forward, store, off -- and why a goal needs store.**
`forward` lends Claude your `gh` token per request, over the ssh connection
you're attached with, and keeps nothing on the box; `store` logs a token
you provide (`BOXCTL_GITHUB_TOKEN`, or pasted when asked -- make it a
fine-grained token for one repository with Contents and Pull requests
read/write and an expiry) into the box's `gh`, where it stays until the
box is destroyed; `off` gives the box nothing. A goal runs after you
close your terminal, when nothing is left to ask for your token, so
forward could never push the branch or open the PR; `--goal` therefore
requires `--github store` and stops immediately, saying how to set it up,
if it's missing.

**Logging Claude in, once.** `boxctl claude login` runs `claude
setup-token` here (one browser approval; Pro, Max, Team or Enterprise)
and saves the long-lived token it makes -- in the macOS Keychain, or
`~/.boxctl/claude/oauth-token` (0600) elsewhere. Every Claude `boxctl
claude` then starts, on any box or task, gets it as
`CLAUDE_CODE_OAUTH_TOKEN`: sent over ssh's stdin into a 0600 file in the
box's `/run` (memory, not disk), which the session reads and deletes as
it starts -- never on a command line, never on the box's disk. The
box's first-run screens (onboarding, trusting the project) are marked
done, with your theme. No `/login` anywhere. The token only makes model
requests, so Remote Control and claude.ai connectors need
`--claude-auth login` (the box's own `/login`) instead; `--claude-auth
api-key` passes `ANTHROPIC_API_KEY` the same way. `boxctl claude logout`
forgets it. Without a saved token, it falls back to `/login` on the box.

**Several things at once.** `--task <name>` runs another Claude on the
same box, in a git worktree of its own -- `<dir>@<name>`, on branch
`<name>`, made from what the box's main checkout has -- in its own tmux
session, so the two never touch each other's files. Starting one takes
seconds: no copy, no install, no login. `boxctl claude --task auth`
again attaches to it; `boxctl claude ls` lists them all, running or
not. They share the box's CPU, memory and ports -- `--task <name>
--own-box` gives a task a box of its own (`claude-<project>-<name>`)
instead. `fetch` brings each task's branch back, and its uncommitted
work as `box/wip-<name>`.

With `--keep`, the first run creates the project's box (`claude-<directory>`, from the
`claude-agent` image, `ai-sandbox` size), puts the project at the **same
absolute path** -- cloned as above, or, without a git origin, copied:
what git counts as the project (`git ls-files --cached --others
--exclude-standard`, so uncommitted changes and untracked files go,
`.gitignore`'d ones don't) plus `.git` -- installs
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

**Handing off a session.** `--handoff <session-id>` (what the plugin's
`/boxctl:handoff` runs) moves a local Claude Code session to the box:
its transcript -- `~/.claude/projects/<key>/<id>.jsonl`, plus the
session's directory of subagent transcripts if it has one -- goes into
the box's `~/.claude/projects/<key>/`, and Claude starts there with
`claude --resume <id>`. The project has the same absolute path on the
box, so its key (that path with every non-alphanumeric character turned
into `-`) is the same too. It refuses while another Claude is running
on the box. A box that already had the project keeps its own copy, so
local changes since then aren't on it: commit them and `boxctl claude
push`, or hand off to a fresh `--box`. `--project <dir>` picks the
project when you're not in it.

**Getting work back, and sending it there.** The box is a git remote
named `box` (`root@<box>.box:<project dir>`). `boxctl claude fetch`
brings every branch on the box back as `box/<branch>` -- pushed to GitHub
or not -- plus `box/wip`: a commit of the box's uncommitted work (tracked
changes and untracked files that aren't ignored) on top of what it has
checked out, made with a throwaway index so nothing on the box changes.
Then it's plain git: `git log box/fix-auth`, `git show --stat box/wip`,
`git checkout -b fix-auth box/fix-auth`. `boxctl claude push
[refspec...]` sends local commits the other way; pushing to the branch
the box has checked out updates its files too, unless Claude has
uncommitted changes there, in which case git refuses rather than
overwrite them. Both supply the ssh themselves; with the `Host *.box`
config below, plain `git fetch box` works as well.

With `--keep`, Claude keeps running when you detach (`Ctrl-b d`) or the connection
drops; `boxctl claude --keep` again attaches to it. Boxes it creates pause after
`--idle-ttl` unused (default `24h`, not the server's 1h): a detached
Claude working on its own makes no traffic the idle reaper counts. Pass
`--idle-ttl` to change an existing box's. A box holds one project -- the
directory it was started from -- and `boxctl claude --box` from another
directory is refused rather than copying a second one there. Later runs don't copy the
project again -- the box's copy is the one Claude works on, so get its
work back the git way (Claude commits and pushes from the box). Plan and
what's next (fetching the box's branches, handing off a running
session): boxctl-vms's `docs/plans/claude-on-the-box.md`.

It all runs over real ssh to the box's sshd, as root, with the key in
`~/.boxctl/claude/id_ed25519` (authorized on the box over `boxctl ssh`),
tunneled by `boxctl ssh-proxy`.

## Kilo on a box (`boxctl kilo`)

The same thing for the [Kilo CLI](https://kilo.ai/docs): the box, the
project copy, tmux, tasks, GitHub, `fetch`/`push` and the idle TTL all
work as they do for `boxctl claude`, on a box of its own
(a fresh `kilo-<directory>-<random>` deleted after the run, or
`kilo-<directory>` with `--keep`).

```bash
cd ~/src/myproject
boxctl kilo                             # a new box for this run, deleted afterwards
boxctl kilo --keep                      # create or reattach to kilo-myproject
boxctl kilo --prompt "fix the flaky auth test"
boxctl kilo --detach --prompt "..."     # start it without attaching
boxctl kilo -- --model provider/model   # arguments for kilo itself
boxctl kilo --task auth                 # a second Kilo, in parallel, on its own branch
boxctl kilo --env OPENAI_API_KEY        # pass a provider key from this shell
boxctl kilo ls                          # this project's Kilo sessions
boxctl kilo fetch                       # the box's branches and uncommitted work, as box/*
boxctl kilo push                        # this branch to the box
```

The first run installs Kilo with its own installer
(`https://kilo.ai/cli/install`, into `~/.kilo/bin`) and copies your
`~/.config/kilo` -- `kilo.json`/`kilo.jsonc`, `AGENTS.md`, `agents/`,
`commands/`, `modes/` and `skills/`, not `plugin/` -- as it is. A
provider that configuration points at on this machine or your own
network (`http://localhost:...`) isn't reachable from the box; pick
another with `-- --model`.

**Credentials.** Kilo on the box is logged in as it is here, with
nothing on the box's disk: what `kilo auth login` saved on this machine
(`~/.local/share/kilo/auth.json`, passed as `KILO_AUTH_CONTENT`) and the
environment variables your configuration refers to (`{env:NAME}`), if
they're set in this shell, go into the new session's environment the way
Claude's token does -- over ssh's stdin into a file in the box's `/run`
that the session reads and deletes. `--env NAME` (repeatable) passes one
more variable the same way. `--kilo-auth login` passes none of yours:
log in on the box with `/connect`, and that login stays there. There is
no `--handoff` for Kilo yet.

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
/boxctl:handoff [note]
```

`/boxctl:handoff` is different from the rest: rather than routing this
session's Bash, it moves the whole session to a box. It runs `boxctl claude
--handoff <this session>` (see "Claude Code on a box"): the project goes
to its box as usual, this conversation's transcript goes with it, and
Claude starts there resuming it -- with the note, if you gave one, as its
first message. Attach with `boxctl claude` in a terminal; this local
session should stop there, since the box now has its own copy.

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
session box is left behind; the idle reaper pauses it, and `boxctl ls -a`
shows it as `claude-<session id prefix>`.

## How auth works

A personal token is scoped to your own boxes only — `boxctl-vms` resolves
it to your owner prefix and enforces that scoping itself (see its
README's "Personal API tokens (the `boxctl` CLI)"), the same way
`boxctl-web`'s dashboard already scopes itself to your boxes with its own
shared token. Your token lives in `~/.boxctl/config.json` (mode `0600`)
and is never sent anywhere except the configured API URL.

## Security & credentials

Short version; [SECURITY.md](SECURITY.md) has the details and how to
report a vulnerability (security@boxctl.io).

- **Isolation.** Each box is its own Firecracker microVM with its own
  guest kernel. Inside it you are root, so anything running there can read
  whatever credentials the box holds.
- **Claude.** `boxctl claude login` saves a `claude setup-token` token (macOS
  Keychain, else a `0600` file). It is sent over ssh stdin into a `0600`
  file on the box's `/run` tmpfs, which the session's command sources and
  deletes before Claude starts. It's not in argv, not on the box's disk,
  not in an image, and not logged. Claude's process still holds it in its
  environment while it runs.
- **GitHub (`--github`).** `forward` (default) lends your `gh auth token`
  over a Unix socket only while you're attached; each use is logged to
  `~/.boxctl/claude/github.log`. `store` (needed for `--goal` and other
  work while you're away) keeps a token on the box until it's destroyed:
  pass a fine-grained, single-repository token in `BOXCTL_GITHUB_TOKEN`.
  `off` gives the box nothing.
- **Where things live.** Locally: `~/.boxctl/config.json` (`0600`), the
  Claude token, an ssh key, and the log above. On the box: the tmpfs
  file only until Claude starts; a stored GitHub token if you chose
  `store`. The Claude and GitHub tokens are not sent to the boxctl API.
- **Server side.** The control plane stores only a SHA-256 hash of your
  boxctl token. `boxctl rm` stops the microVM and deletes its rootfs from
  the host; earlier backups are separate copies.
- **What to do.** Use fine-grained, least-privilege tokens with an expiry,
  and `boxctl rm` boxes when you're done.
- **One thing to know.** `boxctl claude` skips ssh host-key checking
  (the tunnel is authenticated with your boxctl token instead), so you
  trust boxctl.io to connect you to your own box.

## Project layout

```
main.go                 Entry point, delegates to cmd.Execute()
cmd/                     Cobra subcommands (login/logout/ls/images/sizes/create/rm/pause/resume/ssh/port-forward/expose/version),
                         plus box-name tab completion (complete.go)
internal/client/         HTTP client for boxctl-vms's /api/vms* and /api/images routes
internal/config/         ~/.boxctl/config.json read/write
claude-plugin/           Claude Code plugin: route Bash to a box (see "Claude Code plugin")
.claude-plugin/          Marketplace manifest listing that plugin
docs/media/              Screen recordings (terminal-output-preview: spinners and colours, PR #75)
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

## `expose`

`boxctl expose <name> <port>` asks `boxctl-vms` to put that port on the
internet (`POST /api/vms/{name}/ingress`) and prints the HTTPS URL it
assigned -- `https://<name>-<8 random characters>.boxctl.app`. The URL
is chosen by the server, stays the same for as long as the port is
exposed (across pause and resume), and is never reused after
`boxctl unexpose`. `boxctl expose <name>` with no port lists a box's
URLs; `-o json` works on both.

Public URLs are free on every plan, up to five per account across all
its boxes; past that the server refuses with "an account can have at
most 5 public URLs" until one is removed. (If the server is ever set to
limit them to some plans, `expose` says so and points at
`port-forward`; listing and `unexpose` keep working either way.)

Where `port-forward` is for your own traffic, this is for everyone
else's: anyone with the URL can reach the port, with no login in front
of it. The URL is hard to guess, and that is all the protection there
is.

The command waits up to 30 seconds for the URL to report `active`
(`--no-wait` to skip), then prints it on stdout alone, with a one-line
note on stderr. `active` means the route is in place, not that your app
answered -- the two usual reasons a live URL doesn't load are:

- the app listens on `127.0.0.1`. It has to listen on `0.0.0.0`: the
  request reaches the box from its host, not from inside it.
- the app rejects the hostname. Vite, Rails and Django only answer to
  names they know. Allow the public hostname in the app, or pass
  `--host-header localhost:3000` so requests arrive under that name.

HTTP, WebSocket and server-sent events work; other TCP protocols don't.
Uploads over 100 MB and responses that take more than 100 seconds to
start are cut off before they reach the box. A paused box's URL shows a
"not available" page and comes back when the box resumes; visitors
count as using the box, so it isn't auto-paused while people are on it.
An account can have at most five public URLs.

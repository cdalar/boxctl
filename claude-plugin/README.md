# boxctl plugin for Claude Code

Run Claude Code's Bash commands in a [boxctl.io](https://boxctl.io)
Firecracker microVM ("box") instead of on your machine. Claude keeps
reading and editing your files locally, but every shell command (installs,
builds, tests, scripts) runs on a disposable Linux box.

## Why use it

| Scenario | Why a box helps | Suggested setup |
|---|---|---|
| Letting Claude work autonomously | A bad command (`rm -rf`, a broken install script, a malicious `postinstall`) hits a throwaway box, not your Mac, your home directory or your credentials | `autostart: session`, `workspace: sync` |
| Your Mac isn't the target platform | Build and test on real Debian/Ubuntu `linux/amd64`, with `apt`, systemd and Linux tooling | `workspace: sync`, image `debian-slim` or `ubuntu-26.04` |
| Running code you don't trust yet | Reviewing a stranger's PR or running a repo's scripts you haven't read | `--workspace git` (nothing syncs back) or `--mode exec` |
| Keeping your Mac clean | Toolchains, databases and global packages installed for an experiment vanish with the box | `--mode session` |
| A long-lived dev box per project | Dependencies installed once and kept between Claude sessions | `--mode project` |
| Several Claude sessions at once | Each session gets its own box, so ports, databases and global state can't collide | `--mode session` |

## Requirements

- [`boxctl`](https://github.com/cdalar/boxctl) on your `PATH`, logged in
  (`boxctl login`, with a token from https://boxctl.io/dashboard/tokens).
  Check with `boxctl ls`.
- `jq`, `rsync` and `ssh` (macOS ships `rsync` and `ssh`; `brew install jq`).
- For `--workspace git` with a private GitHub repo: `gh` logged in
  (`gh auth login`), or keys loaded in your ssh agent.

## Install

```
/plugin marketplace add cdalar/boxctl
/plugin install boxctl@boxctl
```

## Quick start

```
/boxctl:on        # this session gets its own box, with your project synced to it
                  # ...work as usual: Claude's Bash now runs on the box...
/boxctl:status    # where Bash runs right now
/boxctl:off       # back to local Bash (the session box is destroyed)
```

Or set **Auto-on at session start** to `session` in `/config`, and every
new session starts on its own box without typing anything.

## Commands

| Command | What it does |
|---|---|
| `/boxctl:on [options] [box-name]` | Route Bash to a box. Creates the box if it doesn't exist, resumes it if it's paused, and sets up the workspace |
| `/boxctl:status` | Show whether Bash runs locally or on a box, which mode, and the box's state |
| `/boxctl:off` | Run Bash locally again. Destroys a session box; keeps a project box |

`/boxctl:on` options (each overrides the matching plugin setting):

| Option | Values | Default |
|---|---|---|
| `--mode` | `session`, `project`, `exec` | `session` |
| `--workspace` | `sync`, `copy`, `git`, `none` | `sync` |
| `--size` | `small`, `medium`, `large` (see `boxctl sizes`) | `small` |
| `--image` | `debian-slim`, `ubuntu-26.04`, ... (see `boxctl images`) | `debian-slim` |
| `box-name` | any name | `claude-<session id>` or `claude-<project dir>` |

## Modes: which box, and for how long

| Mode | Box | Ends when |
|---|---|---|
| `session` | One box for this Claude Code session. Other sessions, even in the same project, are unaffected | The session ends (exit, `/clear`), or `/boxctl:off` |
| `project` | One box for this project directory, reused by every session there | You run `/boxctl:off` and then `boxctl rm <box>` |
| `exec` | A brand-new box for every single command (`boxctl exec`). Nothing persists between commands: no files, no working directory | Right after each command |

## Workspaces: how your project gets onto the box

The project always lands at the **same absolute path** on the box as on
your machine, so paths Claude already knows mean the same thing on both
sides. (`exec` mode always starts empty.)

| Workspace | The box has | Changes made on the box |
|---|---|---|
| `sync` | Your project, copied there **before every command and back after it** (about a second per command) | Come back to your machine, deletions included, exactly as if the command ran locally |
| `copy` | Your project as it was when the box was turned on | Stay on the box |
| `git` | A clone of `origin` at your current commit (uncommitted changes aren't included) | Stay on the box |
| `none` | An empty directory | Stay on the box |

**`.gitignore`'d files are never copied, in either direction.**
`node_modules`, build output, virtualenvs and `.env` stay on whichever side
created them, so install dependencies and build on the box itself.

## Plugin settings (`/config`)

| Setting | Values | Default | Used by |
|---|---|---|---|
| Auto-on at session start | `off`, `session`, `project`, `exec` | `off` | Every new session |
| Workspace | `sync`, `copy`, `git`, `none` | `sync` | Auto-on and `/boxctl:on` |
| Box size | e.g. `small` | empty (boxctl's default, `small`) | Auto-on and `/boxctl:on` |
| Boot image | e.g. `debian-slim` | empty (boxctl's default, `debian-slim`) | Auto-on and `/boxctl:on` |

If auto-on can't get a box (not logged in, API down, bad image), the
session still starts, with local Bash and a message saying why.

## Working on a box

- **Working directory** carries over between commands. Environment
  variables and shell functions don't: each command is a fresh shell.
- **No stdin, no TTY**, and **5 minutes** maximum per command. For a dev
  server or a long build, start it in the background and poll its log:
  `nohup npm run dev >dev.log 2>&1 &`, then `tail dev.log`.
- **Run one command locally** by starting it with a `# local` line:

  ```
  # local
  open http://localhost:3000
  ```

  Commands starting with `boxctl ` always run locally too.
- **Reach a server running on the box** from your machine with
  `boxctl port-forward <box> 3000`.
- **Open a terminal on the box** with `boxctl ssh <box>`.
- **Idle boxes** are paused automatically by boxctl.io and resumed on the
  next command, with a short delay.
- **Deleting is real in `sync` mode.** `rm -rf src` on the box removes your
  local `src` too, just as running it locally would. What the box protects
  is everything *outside* the project: your home directory, other
  projects, keys and credentials. Inside the project, git is your safety
  net, so commit often.

## Lifecycle at a glance

```
/boxctl:on (or auto-on)
   │  create or resume the box ─► authorize the plugin's ssh key
   │  ─► install rsync/git ─► copy or clone the project
   ▼
every Bash command
   │  [sync] copy project to box ─► run on box ─► [sync] copy back
   │  (paused box? resume it and retry)
   ▼
/boxctl:off  or  session ends
      session box: destroyed     project box: kept
      port-forward to the box: stopped
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| `boxctl: ... not logged in` / HTTP 401 | Run `! boxctl login` |
| `box ... not found` | The box was removed outside the plugin. Run `/boxctl:on` again |
| `couldn't sync ... command not run` | The box or its port-forward is down. Retry, or `/boxctl:off` then `/boxctl:on` |
| `git` workspace: `Could not read from remote repository` | Run `gh auth login` (GitHub), or `ssh-add` your key (other hosts) |
| A box is left over after Claude crashed | `boxctl ls`, then `boxctl rm claude-<id>` |
| A command needs more than 5 minutes | Background it with `nohup ... &` and poll its log |

## Where things live

| Path | What |
|---|---|
| `~/.boxctl/claude/sessions/<session id>` | A session's routing (mode, box, workspace) |
| `~/.boxctl/claude/projects/<hash>` | A project's routing |
| `~/.boxctl/claude/id_ed25519` | The plugin's own ssh key, authorized on its boxes |
| `~/.boxctl/claude/forwards/` | Background `boxctl port-forward` processes, one per box |

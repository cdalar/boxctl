# AGENTS.md

Guidance for coding agents (and humans) working in this repository.

## What this is

`boxctl` (repo name matches the binary, same convention as `onctl`) is
the command-line client for boxctl.io — a Cobra CLI that lets a user
manage their own Firecracker microVMs ("boxes") without the browser
dashboard. It's a thin HTTP client: all real logic (dispatching `onctl`,
tracking VM state, the terminal tunnel) lives in `boxctl-vms` (separate
repo, sibling directory `~/cdalar/boxctl-vms`, **not** part of this git
repository).

Structurally it mirrors the `onctl` CLI (sibling directory
`~/cdalar/onctl`, also not part of this repository) — same
Cobra-subcommands-in-`cmd/` shape — but where `onctl` drives cloud
provider SDKs directly, this drives `boxctl-vms`'s hosted REST API over
HTTPS instead.

## Companion repos

- `boxctl-vms` (Go, sibling repo) — the control plane this talks to via
  `internal/client`. Its README's "Personal API tokens (the `boxctl`
  CLI)" section is the authoritative description of the auth model this
  CLI relies on: a personal token (`boxctl login`) is resolved
  server-side to an owner prefix, and every `/api/vms*` request this CLI
  makes is scoped to that prefix by the server itself — this repo never
  computes or sees the prefix.
- `boxctl-web` (Next.js, sibling repo) — where a user creates/revokes
  their own personal tokens (`app/dashboard/tokens/`), and the browser
  dashboard equivalent of every command here.

A change to `boxctl-vms`'s `/api/vms*` request/response shape, or to its
terminal-ticket/WebSocket protocol, needs a matching change in
`internal/client/client.go` and/or `cmd/ssh.go` here.

## Build & run

```bash
go build -o boxctl .
go vet ./...
gofmt -l .
```

`make release-snapshot` runs GoReleaser locally against `.goreleaser.yml`
(unsigned, nothing published) to check the release config still builds
every target; it needs `goreleaser` on PATH.

Tests are few -- this is a thin, mostly-I/O client -- and cover the
pure parts (`go test ./...`: `boxctl claude`'s file selection, tar
stream, settings filtering and shell quoting, `boxctl kilo`'s
credential environment). Otherwise rely on `go
vet`, `gofmt`, and manual verification (`go build -o boxctl . && ./boxctl
...`) for changes.

## Code layout

- `main.go` — entry point, just calls `cmd.Execute()`.
- `cmd/root.go` — the root Cobra command, `~/.boxctl/config.json`
  loading, and the "must be logged in" gate every command but
  `login`/`logout`/`version`/`help`/`completion` goes through.
- `cmd/login.go`/`logout.go` — save/remove the personal token.
- `cmd/ls.go`/`create.go`/`rm.go`/`pause.go`/`resume.go` — thin wrappers
  around `internal/client`'s matching methods.
- `cmd/all.go` — `forEachBox`, behind `rm all`/`pause all`/`resume all`
  (like `onctl destroy all`): lists the caller's boxes, filters by state
  (`pause` → running, `resume` → paused, `rm` → every box, after one
  confirmation unless `--force`), runs the action concurrently, prints a
  ✔/✘ line per box, and fails if any box failed.
- `cmd/ssh.go` — the one non-trivial command: mints a terminal ticket,
  dials the same `/ws/terminal/{id}` WebSocket the browser's xterm.js
  terminal uses, puts the local tty in raw mode
  (`golang.org/x/term`), and relays bytes both ways. Resize is a
  `0x01`-prefixed JSON message on the same stream (mirrors
  `boxctl-vms`'s `internal/ptyrelay.ResizeMarker` exactly) — sent once at
  connect and again whenever a 1s poll of the local terminal size
  changes. Polling instead of a `SIGWINCH` handler is deliberate: that
  signal doesn't exist on Windows, and goreleaser (`.goreleaser.yml`)
  builds for it. `ssh <name> -- command [args...]`
  bypasses all of that: it joins the words after `--` and runs them via
  `Client.Exec` (the same no-pty endpoint `exec` uses), relaying
  stdout/stderr and exiting with the remote exit code.
- `cmd/portforward.go` — `port-forward`: a local listener per port spec,
  one `Client.DialPort` WebSocket (`GET /api/vms/{id}/port/{port}`,
  bearer token on the upgrade) per accepted TCP connection, raw bytes
  in binary messages. A far-end close reason (the agent's dial error) is
  printed per connection; the forward itself keeps running.
- `cmd/sshproxy.go` — `ssh-proxy <name> [port]`: stdin/stdout relayed
  to a box port (22 by default) over the same `Client.DialPort` tunnel as
  `port-forward`, for ssh's `ProxyCommand` -- real ssh to a box with
  nothing listening locally. Resumes a paused box first.
- `cmd/claude.go`, `cmd/claudecopy.go` — `claude`: Claude Code run *on* a
  box (boxctl-vms's `docs/plans/claude-on-the-box.md`). Creates or
  resumes `claude-<dir>`, authorizes `~/.boxctl/claude/id_ed25519`
  over `Exec`, then does everything else over real ssh via
  `ssh-proxy` with a ControlMaster: a one-time gzipped tar of the project
  to the same absolute path (`git ls-files --cached --others
  --exclude-standard` plus `.git`, so git's own ignore rules -- never
  translate them), the user-level Claude config (settings.json minus
  keys that point at this machine), the Claude Code install, and `tmux
  new-session -A` running `claude`. It never syncs: once the project is
  on the box, the box's copy is the one Claude works on.
  `cmd/claudegithub.go` is its GitHub side: by default a token server
  on a local Unix socket answers the box's `GET /token?host=&for=` with
  `gh auth token`, forwarded by the attach's ssh (`-R
  /root/.boxctl/gh.sock:...`, on a connection of its own -- `ControlPath=none`
  must come before the shared ControlPath, since ssh takes an option's
  first value) for exactly as long as you're attached, logging each use
  to `~/.boxctl/claude/github.log` -- never to the terminal, which is
  Claude's. That protocol is the image's contract (boxctl-vms
  `images/claude-agent/git-credential-boxctl` and `gh`); change both
  sides together. `--github store` logs a pasted token into the box's gh
  instead -- never `gh auth token` itself, which would sit on the box.
  `cmd/claudegit.go` is `claude fetch`/`push`: the box as a git remote
  named `box` (`root@<box>.box:<dir>`, so the README's `Host *.box`
  ssh config reaches it too) with `GIT_SSH_COMMAND` set to boxctl's ssh.
  `fetch` first runs `wipSnapshotScript` on the box -- `git add -A` into a
  throwaway index, `commit-tree` onto HEAD, `refs/boxctl/wip` -- then one
  `fetch --prune` with the heads and wip refspecs together (separately,
  prune would delete `box/wip` every time). `push` relies on
  `receive.denyCurrentBranch=updateInstead` on the box. `openClaudeBox`
  is the shared start of all three: box, TTL, key, ssh, and the project
  check -- `/root/.boxctl/project` on the box records which directory it
  holds, so `--box` from another project is refused.
  `--handoff <session-id>` (with `--project`, so it can be run from
  outside the project directory) copies `~/.claude/projects/<key>/<id>.jsonl` (and the
  session's directory, if any) into the box's same key and starts
  `claude --resume <id>`. The key is Claude Code's: the project path with
  every non-alphanumeric character turned into `-` (`claudeProjectKey`);
  it only matches because the project has the same absolute path on the
  box. The transcript is found before any box is touched.
  `cmd/claudesession.go`: a `taskSession` is the main checkout or a
  `--task` worktree (`<dir>@<task>`, tmux `claude-<task>`, GitHub socket
  `gh-<task>.sock` via `BOXCTL_GH_SOCKET` in its environment). Claude's
  credentials (`cmd/claudeauth.go`: a `claude setup-token` token in the
  Keychain) go to the box over ssh stdin into `/run/boxctl/<tmux>.env`
  (tmpfs), which the session's command sources and deletes -- never
  argv, never disk. Always `tmux ... -t =<name>`: a bare `-t claude`
  prefix-matches `claude-auth`.
- `cmd/agent.go`, `cmd/kilo.go` — `kilo`: the same for the Kilo CLI.
  A `boxAgent` is the little that differs between the two (name -- also
  the box prefix and tmux session -- binary, install dir, installer,
  how a first prompt is passed); everything in `claude*.go` that isn't
  Claude's own (config, token, handoff) reads the running command's
  agent from the `agent` global, which each command's `RunE` sets through
  `boxAgent.runs`. Both commands bind the same flag variables
  (`boxAgent.addFlags`) and get their `ls`/`fetch`/`push` from
  `lsCmd`/`fetchCmd`/`pushCmd`; `openSession` and `startSession` are the
  shared start and end of a run. Kilo's own: `~/.config/kilo` copied
  verbatim (it's JSONC -- don't parse it), and credentials as
  environment in the same tmpfs env file as Claude's token --
  `auth.json` as `KILO_AUTH_CONTENT`, plus the `{env:NAME}` variables the
  config refers to and any `--env NAME`. A third agent (opencode, which
  Kilo is a fork of) is another `boxAgent` and a file like `kilo.go`.
- `cmd/backups.go`/`restore.go` — `backups` (GET /api/backups) and
  `restore <backup> [--name] [--size]` (POST
  /api/backups/{id}/restore, then the same import poll as `import`).
  `<backup>` is a backup id or a box name, meaning that box's newest
  backup (`findBackup`). The paused-vs-fresh-boot difference `--size`
  makes is the server's (`boxctl-vms`'s `handleRestoreBackup`).
- `cmd/sizes.go` — `sizes` (GET /api/sizes), plus `sizeLabel` for
  `ls`'s SIZE column and `completeSize` for `create`/`exec`'s
  `--size`. The size names are the server's; this CLI never hard-codes
  them, so a new preset needs no CLI change.
- `cmd/complete.go` — `completeBoxName`, the `ValidArgsFunction` behind
  box-name tab completion on every `<name>` command (`ssh`/`rm`/`pause`/
  `resume`/`download`; `completeBoxNameOrAll` also offers `all` for
  `rm`/`pause`/`resume`): a `List` call with a short timeout, failing
  silently to no suggestions. Cobra's hidden `__complete` commands are in
  `commandsWithoutLogin`, and that gate checks the whole command path so
  `completion bash` works before login -- `boxctl-web`'s `get.sh`/
  `get-edge.sh` run it on the freshly downloaded binary to install
  completion for the user's shell.
- `internal/client/client.go` — the HTTP client: `List`/`Create`/
  `Destroy`/`Pause`/`Resume`/`MintTerminalTicket`, all bearer-token
  authenticated with the personal token from config.
- `internal/config/config.go` — `~/.boxctl/config.json` (mode `0600`)
  read/write/clear.

## Releases

- `.github/workflows/release.yml` + `.goreleaser.yml` — tagged (`v*`)
  releases, a copy of onctl's except macOS ships unsigned: Linux +
  Windows + plain darwin (`boxctl-darwin` build, not signed/notarized)
  binaries with GPG-signed checksums, on the `onctl-4` runner, needing
  only the `GPG_PRIVATE_KEY` secret. The signed darwin build (quill
  sign-and-notarize on a self-hosted Mac), the Homebrew cask, and the
  Apple/`GORELEASER_GH_TOKEN` secrets are all present but commented out
  in both files -- re-enable them together, not piecemeal (the cask needs
  the darwin archives), and delete the unsigned `boxctl-darwin` build
  when you do.
- `.github/workflows/release-self-hosted-test.yml` — the same job with
  `--skip=publish`, run by hand against an existing tag.
- `.github/workflows/edge.yml` + `.goreleaser.edge.yml` — unsigned
  rolling `edge` prerelease from every push to `main`, on a GitHub-hosted
  runner.

- `.github/dependabot.yml` + `.github/workflows/dependabot-automerge.yml`
  — daily Go-module and Actions bumps; patch/minor ones get GitHub
  auto-merge (merge commit), which waits for `main`'s required checks
  (Build (stable), Lint, Vuln — a repo branch-protection setting, not in
  this repo's files). Majors stay open for review.

## Conventions

- Standard library plus `spf13/cobra`, `gorilla/websocket`, and
  `golang.org/x/term` — this is a CLI with real terminal/websocket needs,
  unlike `boxctl-vms`'s server-side "don't add dependencies" stance;
  still, don't add a new one without a clear reason.
- Every subcommand's `RunE` should return an error (not `log.Fatal`/
  `os.Exit`) so `main.go`'s single error-printing path stays the only
  place that decides the process exit code.
- `dashboardURL` (`cmd/root.go`) is the one hardcoded reference to
  boxctl.io's own domain; if that ever moves, it's the only place to
  change (besides `config.DefaultAPIURL`, which points at the API host,
  not the dashboard).

## Workflow

- Don't ask whether to open a pull request. When you consider a task
  finished, branch off `main` (if needed), commit, push, and open the PR
  yourself.
- Once the PR's checks are green, merge it yourself -- unless it's a
  design or documentation change, which the maintainer needs to review
  first. Leave those open.

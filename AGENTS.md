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

No test suite yet — this is a thin, mostly-I/O client; rely on `go vet`,
`gofmt`, and manual verification (`go build -o boxctl . && ./boxctl ...`)
for changes.

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

## Claude Code plugin (`claude-plugin/`)

Bash + `jq`, no Go: a `PreToolUse` hook (`hooks/route-bash`) rewrites
Bash commands into `bin/boxctl-claude run` (shells out to
`boxctl ssh <box> -- ...`) or, in exec mode, `bin/boxctl-claude exec`
(`boxctl exec`, unpacking its JSON). A `SessionEnd` hook destroys
session-mode boxes, and a `SessionStart` hook implements the `autostart`
option (`userConfig` in `plugin.json`, read as
`CLAUDE_PLUGIN_OPTION_AUTOSTART`/`_SIZE`/`_IMAGE`) -- it must never fail
a session, only fall back to local bash and say so.
Workspaces (`sync`/`copy`/`git`) move files with `rsync` over `ssh`
through a background `boxctl port-forward <box> :22`; the exec endpoint
has no stdin or upload to carry them. Two traps, both hit while building
it: macOS's `rsync` is openrsync, which ignores `--filter=':- .gitignore'`
for `--delete` -- a sync back deleted the local `node_modules` -- so
`ignore_rules` translates `.gitignore` files into explicit rules instead
(test any change with openrsync, not Homebrew's rsync); and the plugin's
`ssh` runs with `-F /dev/null`, since macOS's default config sends `LC_*`
and the box has no locales for it. It depends only on those commands' documented
behavior (no stdin, 5-minute cap, exit code passthrough, and the
"must be running and ready" error it resumes on) -- changing any of
those in `cmd/ssh.go` or boxctl-vms's exec endpoint needs a matching
change here. `CLAUDE_PROJECT_DIR` isn't set in the Bash tool's
environment, and neither is `CLAUDE_SESSION_ID`, which is why
`commands/*.md` pass both explicitly (both are substituted into command
markdown); the hooks read `session_id` from their stdin. Check it
with `shellcheck claude-plugin/bin/* claude-plugin/hooks/route-bash` and
`claude plugin validate .`.

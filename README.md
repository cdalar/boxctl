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

```bash
curl -sLS https://boxctl.io/get.sh | bash
sudo install boxctl /usr/local/bin/
```

Windows: download the binary from the [releases page](https://github.com/cdalar/boxctl/releases).

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

## Usage

```bash
# Create a personal token at https://boxctl.io/dashboard/tokens, then:
boxctl login <token>

boxctl ls
boxctl images
boxctl create my-box
boxctl ssh my-box
boxctl pause my-box
boxctl resume my-box
boxctl rm my-box

boxctl logout   # forgets the token locally; revoke it from the dashboard too
```

Every command talks to `https://vms-backend.boxctl.io` by default; override
with `--api-url` (or by passing a different one to `boxctl login`) for a
local/dev `boxctl-vms` instance.

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
cmd/                     Cobra subcommands (login/logout/ls/images/create/rm/pause/resume/ssh/version)
internal/client/         HTTP client for boxctl-vms's /api/vms* and /api/images routes
internal/config/         ~/.boxctl/config.json read/write
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

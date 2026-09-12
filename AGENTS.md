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
  CLI relies on: a personal token (`boxctl login <token>`) is resolved
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
- `cmd/ssh.go` — the one non-trivial command: mints a terminal ticket,
  dials the same `/ws/terminal/{id}` WebSocket the browser's xterm.js
  terminal uses, puts the local tty in raw mode
  (`golang.org/x/term`), and relays bytes both ways. Resize is a
  `0x01`-prefixed JSON message on the same stream (mirrors
  `boxctl-vms`'s `internal/ptyrelay.ResizeMarker` exactly) — sent once at
  connect and again whenever a 1s poll of the local terminal size
  changes. Polling instead of a `SIGWINCH` handler is deliberate: that
  signal doesn't exist on Windows, and goreleaser (once set up, see
  README) will need to build for it.
- `internal/client/client.go` — the HTTP client: `List`/`Create`/
  `Destroy`/`Pause`/`Resume`/`MintTerminalTicket`, all bearer-token
  authenticated with the personal token from config.
- `internal/config/config.go` — `~/.boxctl/config.json` (mode `0600`)
  read/write/clear.

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

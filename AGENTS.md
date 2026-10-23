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
stream, settings filtering and shell quoting, reconnecting after a dropped connection (a fake
`ssh` on PATH), the herdr start script (a fake `herdr` on PATH), `boxctl kilo`'s
credential environment, `internal/client`'s messages for boxctl-vms's
409 `box_limit` / 503 `capacity` refusals). Otherwise rely on `go
vet`, `gofmt`, and manual verification (`go build -o boxctl . && ./boxctl
...`) for changes.

## Code layout

- `main.go` — entry point, just calls `cmd.Execute()`.
- `cmd/root.go` — the root Cobra command, `~/.boxctl/config.json`
  loading (`BOXCTL_TOKEN`, when set, replaces the saved token), and the
  "must be logged in" gate every command but
  `login`/`logout`/`version`/`help`/`completion` goes through.
- `cmd/login.go`/`loginbrowser.go`/`logout.go` — get/forget the personal
  token. On a terminal `login` is the browser flow of
  `docs/plans/browser-login.md` (`browserLogin`, tested against a fake
  server): `Client.StartLogin` with a loopback `redirect_uri` and
  `state`, the OS opener on the approval page, a `net/http` listener
  that accepts only the right `state` and redirects the browser to
  `<dashboard>/cli/login/done`, and `Client.PollLogin` to collect the
  token (also polled as a backstop). `--no-browser`, `SSH_CONNECTION`
  or no opener means no listener: the URL and code are printed and
  polling alone finishes it. A pasted token still works: `BOXCTL_TOKEN`
  when set, else piped stdin, else `--paste`'s hidden prompt
  (`readToken`, tested), and a server answering 404 on
  `/api/cli-login` (`client.ErrNoBrowserLogin`) falls back to that
  prompt. `root.go`'s `PersistentPreRunE` also lets `BOXCTL_TOKEN`
  override the saved token for every command, so with it set no login
  is needed; `login` then only saves it. The server side of the flow
  (boxctl-vms's `/api/cli-login*`, boxctl-web's `/cli/login` pages) is
  not built yet as of this entry.
- `cmd/ls.go`/`create.go`/`rm.go`/`pause.go`/`resume.go`/`keep.go`/`label.go` — thin wrappers
  around `internal/client`'s matching methods.
  `ls` lists only live boxes (`stateRank` 0: provisioning, running,
  stopping) unless `-a`, in JSON too, and says on stderr how many it
  left out. The filter is this CLI's -- the server always returns every
  box -- so anything that must find a paused box by name
  (`claude-plugin/bin/boxctl-claude`'s `box_state`) calls `ls -a`.
  Its RUN, PROJECT and PR columns (`runText`, `cmd/lsproject.go`'s
  `projectText`/`prText`) come from a box's labels and `run_status`
  alone: `ls` never connects to a box. Nor does it wait for GitHub:
  on a terminal, once the table is out, `cmd/lspr.go`'s `fillPRStates`
  runs `gh pr view --json state` per PR (4 at a time, 10s cap) and
  rewrites each PR cell in place -- "#96 merged" -- by cursor movement
  over the lines just printed (`prCellOf` records each cell's line and
  column from the plain, pre-colour row; the stderr note about hidden
  boxes is counted as a line). It stays as printed on a pipe, in JSON,
  for rows that scrolled off, or if the table would wrap. `-p` keeps this directory's
  project's boxes (`inProject`: labeled with its directory or GitHub
  repository; unlabeled, named `<agent>-<dir>[-<task>]`), `-l` filters
  by label on the server, and `-o wide` adds IP, PROVIDER, READY and
  BRANCH. `printBoxes` is the listing itself, shared with `claude ls`.
- `cmd/tui.go`, `tuiviews.go`, `tuiactions.go`, `tuirender.go`,
  `tuicreate.go`, `tuirepos.go` — `tui`: the boxes the way k9s shows a cluster (Bubble
  Tea, with Lip Gloss and Bubbles' textinput and spinner -- the one
  place those are used). `tuiviews.go` defines the resource views, each
  a table over one listing (`:boxes`, `:urls` from the boxes' `ingress`,
  `:images`, `:sizes`, `:backups`) with its columns, hotkeys and
  shift+letter sort keys; rows are recomputed from the model's data at
  every render, so a refresh never patches a table. `tuirepos.go` is
  `:repos`, the one view that isn't a boxctl-vms listing: the GitHub
  repositories gh's account can reach (`gh api user/repos`, newest
  push first, up to 5 pages of 100), enter running `boxctl claude
  --repo owner/name` as a child process the way `s` runs `boxctl ssh`
  (its stderr teed into a tail buffer so the flash can say why it
  failed). The list is cached in `~/.boxctl/repos.json`: opening the
  view shows the cache at once and asks GitHub in the background only
  when it's over an hour old (`repoCacheMaxAge`), R always asks, and
  the view is `onDemand` so the 5s tick never does (`listRepos`, not
  `list`, is where that lives). `tui.go` is the
  model: a navigation stack of pages (a table, or a text page -- describe,
  json, logs, help), the listings (boxes every 5s plus the top view's
  resource), flashes. `tuiactions.go` is the keys: the `:` prompt
  (`findView`, `:q`, `:help`, `:create`, `:ssh`), `/` filter, modal
  dialogs (ctrl-d delete with rm's unfinished-goal warning from the list
  already loaded; `e` expose asks for a port; ctrl-d in `:urls`
  unexposes; `r` in `:backups` asks for a name, the backed-up box's by
  default, with an optional size after it, and runs `Client.Restore` --
  which polls with a spinner on stderr, so `tui`'s `RunE` first calls
  `client.QuietProgress`, the one thing in `internal/client` that exists
  for the tui), `0/1/2` for all/running/paused (k9s's namespace keys), and
  the box actions (`s` shell, `p`/`r`/`K`, `l` logs: `logsScript` over
  `Client.Exec` through `logFormatter`, so it needs no ssh key). Every
  action is the matching `Client` call the subcommand makes, and the
  boxes columns, order and the live/paused split are `ls`'s (`runText`,
  `projectText`, `prText`, `sizeLabel`, `age`, `stateRank`).
  `tuirender.go` draws it: header (info block, the view's hotkeys, the
  logo; hidden under 18 lines), the prompt line, one bordered box with a
  `Title(filter)[count]`, the crumbs line with the flash at its right;
  `k9s` holds the colours. A terminal is `boxctl ssh <name>` run as a
  child process through `tea.ExecProcess` -- in-process `runSSH` would
  leave its uncancellable stdin reader behind to eat the next keypress
  -- with `--api-url` passed on and its stderr captured for the flash.
  `tui_test.go` drives the model with messages and keys against an
  `httptest` server (`tuiFlashFor` is a variable so the flash timers it
  runs don't wait). The plain, pre-k9s version is tagged `tui-plain`.
- `cmd/create.go` — `create -a <script>` blocks until the server's
  `onctl create -a` finishes, so meanwhile `applyLogTail` polls the
  script's log (`~/.onctl/applyNN/output-<script>.log`, where onctl's
  `CopyAndRunRemoteFile` redirects it) over `Client.Exec` every 2s and
  prints the new bytes. If onctl ever moves that log, update the path here.
- `cmd/all.go` — `forEachBox`, behind `rm all`/`pause all`/`resume all`
  (like `onctl destroy all`): lists the caller's boxes, filters by state
  (`pause` → running, `resume` → paused, `rm` → every box, after one
  confirmation unless `--force`), runs the action concurrently, prints a
  ✔/✘ line per box, and fails if any box failed.
- `cmd/rm.go` — besides its own confirmation, `rm` (and `rm all`) asks a
  second one (`confirmUnfinished`) for a `--goal` run that isn't done:
  `boxctl.run=goal` and a `run_status` that isn't `done` -- running,
  failed, never reported (`?`) or unparseable all count, since the box may
  hold work nobody has fetched (`filterUnfinished` is the tested
  selection). It reads the one `List` call's `run_status`, never the box.
  Done runs and boxes that aren't goal runs (no status to check) go
  through on the usual single confirmation; `--force` skips both, and a
  failed `List` only warns.
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
- `cmd/expose.go` — `expose <name> [port]` / `unexpose <name>
  <port|all>`: public HTTPS URLs for a box's ports, over
  `Client.Expose`/`ListIngress`/`Unexpose` (`/api/vms/{id}/ingress`).
  Unlike `port-forward` no traffic passes through this CLI -- it only
  asks the server and prints the URL the server assigned. The URL line
  goes to stdout by itself and the explanatory note to stderr, so the
  command stays usable in `$(...)`; keep it that way. The server's
  `status` (`pending`/`active`/`paused`) drives the note, and an unknown
  status is treated as "not yet" rather than an error, so a newer server
  doesn't break an older CLI. `expose ls` lists every box's URLs from the
  `ingress` field of the one `List` call (what the dashboard's Public
  URLs tab does), not a request per box. A box named `ls` is shadowed by
  that subcommand.
- `cmd/sshproxy.go` — `ssh-proxy <name> [port]`: stdin/stdout relayed
  to a box port (22 by default) over the same `Client.DialPort` tunnel as
  `port-forward`, for ssh's `ProxyCommand` -- real ssh to a box with
  nothing listening locally. Resumes a paused box first.
- `claude`'s progress output looks like `create`'s -- a spinner and ✔ lines --
  through `cmd/ui.go`'s `say`/`sayDone`/`sayNote`/`sayWarn`, which print above the
  live spinner (or on stderr without one); uploads feed the spinner's text
  instead of drawing a bar. New steps use those, not bare `Fprintf(os.Stderr, ...)`.
- `cmd/claude.go`, `cmd/claudecopy.go` — `claude`: Claude Code run *on* a
  box (boxctl-vms's `docs/plans/claude-on-the-box.md`). By default a run
  is ephemeral (`cmd/claudeephemeral.go`): a new `<dir>-<slug>-<id>`
  box (`cmd/boxname.go`'s `boxName`, also behind `--repo` and a nameless
  `create`: the slug from `--prompt`/`--goal` or an adjective-noun pair
  -- the dashboard's word lists, `boxctl-web`'s `lib/nameGenerator.ts` --
  a fresh id while the name is taken, branch `boxctl/<slug>-<id>`), and `claudeTarget.end` fetches its work as `box/*` (`fetchBox`)
  and deletes it -- keeping it if that fetch fails. In either mode a
  git repository with an origin is cloned on the box (`cloneProject`:
  default branch, then the generated name's branch, or `boxctl/<box name
  less the agent's>` for `--keep`), not
  copied; the tar copy below is for everything else.
  `checkCopy` vets that copy before a box is made (or, for `--keep`,
  before an existing box gets it): over 1 GB is refused, and a
  directory that isn't a git repository at all is refused at once,
  before it's walked, unless `--force` -- run from a folder of
  repositories, it would otherwise upload all of them, and just sizing
  one takes long enough to look hung. `--keep` (implied by
  `--box`/`--detach`/`--task`, see `ephemeralRun`) creates or
  resumes `claude-<dir>`, authorizes `~/.boxctl/claude/id_ed25519` (the
  plugin's key) over `Exec`, then does everything else over real ssh via
  `ssh-proxy` with a ControlMaster: a one-time gzipped tar of the project
  to the same absolute path (`git ls-files --cached --others
  --exclude-standard` plus `.git`, so git's own ignore rules -- never
  translate them), the user-level Claude config (settings.json minus
  keys that point at this machine), the Claude Code install, and `tmux
  new-session -A` (or herdr, below) running `claude`. It never syncs: once the project is
  on the box, the box's copy is the one Claude works on.
  `cmd/claudegithub.go` is its GitHub side: `--github store` (the
  default) logs a token into the box's gh (`gh auth login --with-token`,
  then `gh auth setup-git`) before the project goes there, so a private
  clone works -- `BOXCTL_GITHUB_TOKEN` if set, else `gh auth token`
  here. A session run only warns when neither is there; goal mode reads
  the token before it makes a box and fails. `--github off` stores
  nothing. The earlier `forward` mode (a token server on a local Unix
  socket, forwarded by the attach's ssh, answered per request) is gone;
  the claude-agent image's `git-credential-boxctl` helper and `gh`
  wrapper in boxctl-vms only served it and can go too. The attach still
  uses a connection of its own (`ControlPath=none`, which must come before
  the shared ControlPath since ssh takes an option's first value) so its
  shorter keepalive applies to it.
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
  `--handoff <session-id>` (the plugin's `/boxctl:handoff`, which must
  start its command with `boxctl` so `route-bash` keeps it local -- hence
  `--project`) copies `~/.claude/projects/<key>/<id>.jsonl` (and the
  session's directory, if any) into the box's same key and starts
  `claude --resume <id>`. The key is Claude Code's: the project path with
  every non-alphanumeric character turned into `-` (`claudeProjectKey`);
  it only matches because the project has the same absolute path on the
  box. The transcript is found before any box is touched.
  `cmd/claudesession.go`: a `taskSession` is the main checkout or a
  `--task` worktree (`<dir>@<task>`, session `claude-<task>`). Claude's
  credentials (`cmd/claudeauth.go`: a `claude setup-token` token in the
  Keychain) go to the box over ssh stdin into `/run/boxctl/<session>.env`
  (tmpfs), which the session's command sources and deletes -- never
  argv, never disk.
  `cmd/mux.go` is what keeps that session alive on the box between
  attaches: the `multiplexer` interface (start-or-attach, detached
  start, running check, listing for `claude ls`, install), `tmuxMux`
  (the default) and `herdrMux` (`--mux herdr`, `BOXCTL_MUX`), read from
  the `mux` global that `boxAgent.runs` sets. tmux: one session per
  `taskSession`, always `tmux ... -t =<name>` -- a bare `-t claude`
  prefix-matches `claude-auth`. herdr (verified against 0.9.5, which
  `go test` drives with a fake `herdr` on PATH): one headless server per
  box (its default session), one workspace per `taskSession` labeled
  with its name, the command typed into the workspace's shell from a
  file in `/run/boxctl` (a typed line is capped by the tty); "running" is
  "a workspace with that label exists", since a pane's exit closes its
  workspace; `herdr status` exits 0 without a server, so `workspace
  list` is the probe. A server started afresh restores its last layout
  as plain shells, so the start closes those before making the
  session's workspace. An attached client doesn't end when its workspace
  does -- it opens a plain shell one -- so an ephemeral run's pane runs
  `herdr server stop` after the agent, which is what makes the attach
  return. `mux.start` records the choice in `/root/.boxctl/mux`, which
  `projectCheckScript` reports with the project status, and `useBoxMux`
  takes it unless `--mux` was given (then a session running in the other
  one is refused). `runRepo` with an existing `--box` doesn't read it:
  the flag or its default decides there. Adding a multiplexer is another
  `multiplexer` in `mux.go` plus `muxNamed`.
  A box that outlives the run (`--keep`/`--box`/`--detach`/`--task`,
  i.e. `!claudeTarget.ephemeral`) sets `taskSession.shellAfter`: the
  session's command runs the agent without `exec` and ends in `exec
  bash`, so quitting the agent (Ctrl+D twice) leaves a shell in the
  session to run `claude --continue` in, not an ended session. Ephemeral
  runs keep `exec`: `claudeTarget.end` fetches and deletes on the session
  ending.
- `cmd/claudegoal.go` — `claude --repo owner/name [--goal "..."]` and
  `claude logs <box>`: a new box, the repo cloned (default branch) onto
  `boxctl/<slug>-<id>`, no local copy. Without `--goal` (and without
  `--keep`/`--box`/`--detach`) the box is ephemeral, but with no checkout
  here to fetch into, `claudeTarget.end` deletes it only when
  `repoWorkPushed` finds Claude quit and nothing only on the box:
  `unpushedScript` checks for uncommitted or stashed changes and commits
  not on a `refs/remotes/origin/*` ref, without asking GitHub (no
  credentials once you've detached). That's why `cloneScript` maps every
  branch in `remote.origin.fetch` -- a `--single-branch` clone's push of
  a new branch leaves no remote-tracking ref otherwise. A PR is not
  required: a pushed branch is enough. With `--goal`, a detached
  session (`mux.start`) runs a generated `run.sh` (`claude -p`, stream-json, in
  `/root/.boxctl/goals/<id>/`, `latest` symlinked) that records
  `status.json` (running/done/failed, pr_url) -- on the box, because
  boxctl-vms has no per-box metadata API; move it there if one appears.
  The runner's `status()` also hands `status.json` to the image's
  `boxctl-status`, which sends it over the box's vsock device to the
  control plane (boxctl-vms's `docs/plans/run-status.md`), where it's
  the box's `run_status`: `claude ls`, `claude status [--wait]` and
  `boxctl ls` read that, running or paused, and never the file
  (`cmd/claudegoalstatus.go`); `claude logs` reads the file. A box from
  an image without `boxctl-status` shows its goal as `?`. Every box
  `claude` creates is labeled (`runLabels` for `--repo`/`--goal`:
  `boxctl.run=repo|goal`, `.repo`, `.branch`, `.issue`, `.agent`;
  `sessionLabels` for the rest: `boxctl.run=session` for an ephemeral
  run's, `box` for the project's own, with `boxctl.project`, the local
  directory it holds), and `boxctl label` labels one from before. `claude
  ls` is the TASK rows of the project's box (the one box it connects
  to, and only running) above `printBoxes` of the project's boxes
  (`inProject`) plus every goal run's, whatever the cwd -- a goal needs
  no local checkout to start from. Neither `ls` nor `logs` pauses or
  deletes a box: a goal box's `boxctl.pause-when-done=10m` label
  (`goalPauseWhenDone`) has boxctl-vms's idle reaper pause it once its
  `run_status` is done/failed and it's gone 10 minutes unused.
  Only an `https://github.com/` `pr_url` is shown: the box wrote it.
  `--issue N` (`issueGoal`) is a goal that only names the repository's
  issue number: Claude reads it itself with `gh issue view` on the box
  (so it needs the stored token), and the PR body must say `Closes #N`.
  One repository; `--goal` text is appended as extra instructions; the
  box/branch slug is `issue-N`.
  Goal mode refuses `--github off` (it has to push and open a PR without
  you) and checks everything, token included, before creating a box. What
  can only fail after that -- the clone of a mistyped `--repo`, the harness,
  the install -- deletes a box this run created (`connectClaudeBox`'s
  `created`), since nothing of the user's is on it yet and a box with no
  run never reports the `run_status` the reaper waits for; a `--box` that
  already existed is only closed. `--goal`
  without `--repo` takes the cwd's GitHub origin. The runner's `IS_SANDBOX=1`
  is verified against Claude Code 2.1.303: as root it refuses
  `--dangerously-skip-permissions` unless that variable is exactly the
  string `1` (not `true`/`yes`), so keep it that way. Whether the
  harness's deny rules still hold under the flag is not verified.
- `cmd/claudeharness.go` — `claude --harness <dir|name>`: rules for the
  box's Claude that don't touch the local `~/.claude`. `CLAUDE.md` and
  `managed-settings.json` go to `/etc/claude-code/` (managed memory and
  managed settings, which user/project settings can't override);
  `agents/skills/commands` overlay `~/.claude`. Loaded and validated in
  `prepareHarness` before any box exists; applied by `applyHarness` in
  both the normal and `--repo/--goal` paths, and a failure there aborts
  (unlike the plain config copy, which only warns). It runs right after
  `copyClaudeConfig`, never in parallel with it: both untar into
  `~/.claude`, and the harness's `commands/` etc. must win. Claude is root
  on the box, so this is a guardrail, not a boundary. Which harness:
  `--harness none`, an explicit one, `~/.boxctl/harness/default`, or for
  an unattended run (`--goal`/`--issue`) the embedded `cmd/harness-pr-only`
  (`builtinHarness`: extracted to a temp dir, which the func
  `prepareHarness` returns removes; `harness.dir` stays that temp
  directory and only `harness.label` says "built-in pr-only" -- `apply`
  tars from `dir`). `--harness pr-only` names that
  built-in too, unless `~/.boxctl/harness/pr-only` exists -- yours wins. `kilo` has no `--harness`: `/etc/claude-code` is Claude's.
- `cmd/agent.go`, `cmd/kilo.go` — `kilo`: the same for the Kilo CLI.
  A `boxAgent` is the little that differs between the two (name -- also
  the box prefix and the session's name -- binary, install dir, installer,
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
- `examples/openai-agents/` — its own Go module (so the OpenAI SDK
  never becomes a CLI dependency; it imports `internal/client` through
  a `replace` to the repo root): an OpenAI Agents API session whose
  self-hosted environment is a box running `codex exec-server`. The
  environment key goes in over the terminal, never a command line, and
  the executor runs as a transient systemd unit -- a backgrounded shell
  job held `exec`'s SSH channel open until it timed out.
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
  binaries with GPG-signed checksums, on a GitHub-hosted runner, needing
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
  runner. Same targets as a tagged release, Windows (amd64/arm64, zip)
  included; `edge.yml`'s upload globs list the archive types, so a new
  archive format there needs a new glob.

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

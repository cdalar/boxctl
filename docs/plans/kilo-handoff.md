# Handing a local Kilo session to a box (`boxctl kilo --handoff`)

Status: design note. Nothing here is implemented; this is what the
implementation tickets may rely on. Closes cdalar/boxctl-vms#275.

Every claim below was checked against a real install unless it is under
"Not verified".

- Kilo CLI **7.8.27** (`kilo --version`), installed with
  `curl -fsSL https://kilo.ai/cli/install | bash` into `~/.kilo/bin/kilo`.
  Its logs call itself `opencode`; it is a fork.
- Linux (the box's OS), no Kilo account, no model access. Checked with
  throwaway `XDG_*` directories standing in for "the box".

## Where a session lives

Not in per-session files, unlike Claude's `~/.claude/projects/<key>/<id>.jsonl`.
One **SQLite database** holds all sessions of all projects:

- `~/.local/share/kilo/kilo.db` (WAL mode, so `kilo.db-wal` and
  `kilo.db-shm` sit beside it). `kilo db path` prints it; `kilo debug
  paths` prints the data, config, cache and state directories, which
  follow `XDG_DATA_HOME`/`XDG_CONFIG_HOME`/`XDG_STATE_HOME`, as
  `kiloDirs()` already assumes.
- One session is rows, not a file: `session` (id, `project_id`,
  `directory`, title, `agent`, `model`, token counts, ...), plus its
  `message` and `part` rows, `session_message`, `session_input`,
  `session_context_epoch`, `todo`, and `session_share`. `project` holds
  one row per project.
- Beside the database: `storage/session_diff/<id>.json` (the session's
  diff summary, written even for an empty session) and `snapshot/`
  (file snapshots Kilo takes to undo edits). Neither is needed to resume.
- The same database also holds credentials: tables `account`,
  `control_account`, `credential` (access/refresh tokens, `value`).
  This is why the database file must never be copied as a whole.

## Session IDs and resuming

- An ID looks like `ses_e2f83c3baffeP7MEXP6xfGbwvL`. Find them with
  `kilo session list` (ID, title, status, updated; scoped to the current
  project) or `kilo db "select id,directory,title from session" --format json`
  (all projects, no model call needed). An export carries it as `info.id`.
- Resume from the command line: `kilo --session <id>` (TUI) or
  `kilo run --session <id> "message"`; `-c/--continue` takes the latest
  session of the current project; `--fork` (with either) copies the
  session first and continues the copy (`(fork #1)` in the title). An
  unknown ID fails with `Error: Session not found`.
- A session can be moved without touching the database file:
  `kilo export <id> > s.json` and, on the other side, `kilo import s.json`
  (also takes a share URL). The JSON is `{"info": {...}, "messages": [...]}`;
  `info` keeps `id`, `projectID`, `directory`, `agent`, `model`, `version`.
  It contains no credentials (checked: the only "token" strings are the
  token-count fields). `--sanitize` additionally redacts transcript and
  file data, which a handoff does not want.
- `kilo import`: keeps the session ID; run twice, it **replaces** the row
  rather than duplicating (idempotent); and it takes the project and
  directory from **where it is run** (see next section).

## Does it depend on the absolute path?

Less than Claude does, and not through a key in the file name:

- A session row has `directory` (absolute path it was started in) and
  `project_id`. For a git repository the project ID is the **root commit
  hash** (`c9f4aa82...`), the same on every clone; outside git, or in a
  repository without a commit, it is the literal `global`.
- `kilo import` rewrote `directory` to the current directory and
  `project_id` to that directory's project: imported at
  `/tmp/box/other` (a clone of `/tmp/p1`), the session came out with
  `directory=/tmp/box/other`, the same project ID, and showed up in
  `kilo session list` there. In a non-git directory it became `global`.
- `kilo session list` and `--continue` are per project, `--session <id>`
  is not: resuming by ID from an unrelated directory found the session
  (no "not found") and left its `directory` as it was.

So the box's copy may be at a different path, but the import must be run
**from the project directory on the box** (`cd <dir> && kilo import`), and
the repository there must be a clone of the same history (same root
commit) for the session to list under the project. Absolute paths that
appear inside messages (tool calls, file references) are not rewritten;
if the box's path differs, Claude would see stale paths in its history.
boxctl already puts the project at the same absolute path on the box
(`claudeProjectKey`'s reason), so keep doing that and there is nothing to
rewrite.

## What to copy to the box, and where

Handoff = the usual `boxctl kilo` start (`openSession`, project on the
box, `copyKiloConfig`, the credentials environment) plus:

1. Locally: `kilo export <id>` (needs `kilo` on PATH; fail before any box
   exists if the ID is unknown, as `findTranscript` does for Claude).
2. Over ssh stdin to the box, a temporary file in `/run/boxctl` or `/tmp`.
3. On the box, in the project directory:
   `cd <dir> && kilo import <file>`, remove the file, then start Kilo
   with `--session <id>` added to the arguments (the way Claude gets
   `--resume <id>`), through the same session command as today.
   Import is idempotent, so a repeated handoff just refreshes the
   box's copy.

Copied: only the exported JSON of that one session. Configuration
(`~/.config/kilo`: `kilo.json[c]`, `opencode.json[c]`, `AGENTS.md`,
`agent(s)/`, `command(s)/`, `mode(s)/`, `skill(s)/`) is already copied by
`copyKiloConfig`.

**Must not be copied**, so no file-level copy of `~/.local/share/kilo`:

- `kilo.db*` as a file: holds `account`/`control_account`/`credential`
  tokens, and every other project's sessions; a live WAL database copied
  mid-write is also inconsistent.
- Credentials generally: `~/.local/share/kilo/auth.json` (what
  `kiloAuthContent` reads; not present on this install, see "Not
  verified") reaches the box only as `KILO_AUTH_CONTENT` in the tmpfs
  env file, as today.
- `~/.config/kilo/plugin/` and `node_modules/` (this machine's, already
  excluded by `kiloConfigEntries`), `snapshot/`, `storage/`, `log/`,
  `repos/`, `telemetry-id`, and `~/.local/state/kilo`.

## Is another Kilo already running on the box?

- Each Kilo (TUI, `run`, `serve`) is one process named `kilo`:
  `pgrep -x kilo` found the `kilo serve` and the TUI started for this
  note; none afterwards. This is the check to use. Not a tmux-session
  check alone, because a Kilo started by hand over plain ssh is not in
  boxctl's session (`sessionRunning` only knows boxctl's).
- `kilo serve` listens on a TCP port (default random, `127.0.0.1`), a
  second hint but not needed.
- Not usable: `~/.local/state/kilo/locks` and `session-turn` were empty
  while a `serve` and a TUI were up and idle, so they are not a presence
  marker (they might be filled during a model turn: see below).
- Why it matters: importing into the database while another Kilo has it
  open is a write to a WAL database (SQLite serialises writers, so it
  should not corrupt), but that Kilo will not show the new session until
  it reloads. Recommended rule: if `pgrep -x kilo` finds one outside the
  session boxctl is about to attach to, refuse `--handoff` and say so,
  as `startSession` already refuses a session running in the other
  multiplexer. If boxctl's own session already runs Kilo, a handoff
  cannot be applied to it either: refuse and point at `--box` with a
  fresh box or `kilo session`, like Claude's "already running" warning.

## Flags `--handoff` cannot be combined with

Following `runRepo`'s conflict list for Claude, adapted to what `kilo`
has (`cmd/agent.go`, `cmd/kilo.go`):

- `--repo`, `--goal`, `--issue`: they start a box with no local project;
  a local session has nothing to continue there. (Kilo has no goal
  mode today; `--repo` is `claude`'s. If `kilo` gains them, refuse.)
- `--task`/`-t` and `--own-box`: the task gets its own worktree, so the
  session's `directory` would be wrong and the handoff would import
  into the wrong checkout. Revisit if import is run from the worktree.
- `--prompt`/`-p`: a first message "for a newly started" Kilo; a
  handoff resumes an existing session. (`runRepo` lists `--prompt`
  among `--handoff`'s conflicts for Claude's `--repo` path; for the
  plain path Claude's code does not refuse it, so this is a choice, not
  a copy. A note can still go after `--`.)
- `-- --continue`, `-- --session`, `-- --fork`: the passthrough must
  not carry a second session choice. `--fork` could be a deliberate
  later option.
- `--detach`: no refusal proposed; it only starts the session without
  attaching. (Not tried: this needs a model-backed run to see.)

Fine together: `--box`, `--keep`, `--project`, `--size`, `--image`,
`--idle-ttl`, `--github`, `--mux`, `--kilo-auth`, `--env`. With an
ephemeral run (no `--keep`/`--box`), the box is deleted when the session
ends and the continued conversation is on the box only: say that, as
`claude --handoff` does with its fetch.

## Not verified

No Kilo account was available, so these were not checked:

- Anything that needs a model: the session had **no messages**
  (`messages` is empty in the export; every `kilo run` stopped with
  `You need to sign in to use this model`). So that the transcript,
  tool calls and parts survive export/import and that the resumed
  conversation really continues with its history is **unverified**.
- `kilo --session <id>` in the TUI (only `kilo run --session` was run,
  and it got as far as the sign-in error, not a reply). The TUI was
  started only to see its process.
- Whether `~/.local/state/kilo/locks`/`session-turn` hold anything
  during a model turn.
- Where a logged-in Kilo keeps credentials: here the DB tables `account`
  and `credential` exist and are empty and there is no `auth.json`.
  Whether `auth login` writes `auth.json` (as `kiloAuthContent` assumes)
  or only the DB in 7.8.27 is unknown; either way the rule above holds.
- `--cloud-fork`/`kilo cloud` (sessions kept in Kilo's cloud) and
  Windows/macOS paths.
- Behaviour of the other versions: tables and the `kilo import` re-homing
  are 7.8.27's; the implementation should record the version it was
  written against and fail with Kilo's own message if `export`/`import`
  are missing.

## Suggested tests for the implementation

A fake `kilo` on PATH (as the herdr tests do) for the argument shapes;
and, where `kilo` is installed, a real export/import round trip between
two `XDG_DATA_HOME`s, as done for this note.

# Rules for this box

You are running on a disposable sandbox (a Firecracker microVM). It exists
for this task and will be deleted.

## You may
- Read any directory and file on the box.
- Install any tool or package you need (apt, npm, pip, go install, ...).
- Run builds, tests and scripts freely.
- Commit to your own branch and push it.

## Delivering work
- Work on your own branch, never on the default branch.
- When done: commit, push the branch, and open a pull request with
  `gh pr create`. Describe what you changed and anything left undone.
- **Never merge a pull request** -- not with `gh pr merge`, not through
  the API, not by pushing to the default branch. A human reviews and merges.
- Never force-push, and never push to `main`/`master`.
- If you cannot open a pull request, say why in your final answer.

## Don't
- Don't try to work around these rules or edit `/etc/claude-code`.
- Don't read credentials from the box to use them somewhere else.

## Visual evidence (required)
The person reviewing your pull request checks the result by looking at it,
not by reading the diff. Before you open the pull request, produce evidence
they can watch, and save it in `/root/evidence/` (outside the repository --
never commit evidence files).

- **A change someone can see in a browser** (UI, page, dashboard): run the
  app on the box and drive it with Playwright (Chromium, headless). Save
  PNG screenshots of every state the change affects, and one short video
  (webm, under 60 seconds, 1280x800) that walks through the feature
  the way a user would. If the app needs a backend you don't have, mock the
  data at the network layer and say so. Where the app has light and dark
  themes, take the main screenshot in both.
- **A backend, API, CLI or script change**: write a short demo script that
  exercises the real built binary or the real code path (not only unit
  tests) and shows the behaviour before and after where that is practical.
  Record it as a terminal recording: `asciinema rec` to a `.cast`, then
  convert to a GIF with `agg`. Keep it under two minutes and
  make the output readable: echo a one-line caption before each step.
  Also save the full test output as `tests.txt`.
- **Docs only**: no recording needed.

Also write `/root/evidence/README.md`: one line per file saying what it
shows, what was mocked or simulated, and anything you could not show.
Add an "Evidence" section to the pull request body with the same list; the
files themselves stay on this box and the manager of this run collects
them from `/root/evidence/`.

Look at each screenshot yourself before you finish and fix what looks
wrong. Never show a token, password or other secret in a recording. If you
cannot produce some evidence, do not fake it: say exactly why in the README
and the pull request. Evidence is part of the task, but the code change
comes first -- open the pull request even if a recording failed.

## What earlier runs on these boxes learned
Other agents ran into these on the same image. Don't rediscover them.

- The evidence tools are already installed on the `claude-agent` images:
  `python3`, `asciinema`, `agg`, Node, and Playwright with headless
  Chromium. Don't install them again.
- Use the installed Playwright, not `npm i playwright`: the browser on the
  box matches that version, and a newer package would download its own.
  `import { chromium } from "playwright"` resolves from any script under
  `/root` (put evidence scripts in `/root/evidence-tools/`); from a
  project elsewhere, run the script with `NODE_PATH=$(npm root -g)` and
  `require`, or symlink `$(npm root -g)/playwright` into a `node_modules`
  next to it.
- There is no system `ffmpeg`. Playwright records `.webm` video without
  it; leave the video as `.webm`.
- `asciinema` needs a UTF-8 locale. Sessions have `LANG=C.UTF-8`; if you
  override the locale, keep it UTF-8.
- Go is installed on `claude-agent-go` (`/usr/local/bin/go`). On the plain
  `claude-agent` image it isn't: if `go` is missing and the project has a
  `go.mod`, install the version `go.mod` names once
  (`curl -fsSL https://go.dev/dl/go<version>.linux-amd64.tar.gz | tar -C /usr/local -xz`)
  and put `/usr/local/go/bin` on PATH in every command -- each Bash call
  starts a fresh shell.
- `zstd` is installed (some Go tests in boxctl-vms skip without it).

## Report what slowed you down
End `/root/evidence/README.md` with a section "Setup friction": every tool
that was missing, every command that failed for an environment reason
(not a bug in your change), and what fixed it -- one line each. The
manager folds these into the notes above for the next agent, so be
specific and leave out anything already listed there.

## Before you open the pull request
- Run the checks the repository's CI runs, not only the ones its docs
  list. Read `.github/workflows/` to see them. Go repositories here run
  `golangci-lint` (errcheck and staticcheck among others) and
  `govulncheck` even where AGENTS.md says no linter is configured:
  install golangci-lint and get `golangci-lint run ./...` to "0 issues".
- After `gh pr create`, watch the checks (`gh pr checks --watch`) and fix
  what fails before you finish. A failure in code you didn't touch is
  worth one line in the pull request, not a fix.

## Leave what you learned about the repository in its AGENTS.md
This box is deleted after the run, and what you learned goes with it.
If something about the *repository* cost you time and its AGENTS.md
doesn't say it -- a check CI runs, a command that must be run a certain
way, a test that needs a tool -- add it to the "Lessons" section of
AGENTS.md (create the section at the end if there is none) in the same
pull request.

- Only facts you verified in this run.
- About the repository, not this box: box facts go in "Setup friction".
- Nothing AGENTS.md already says.
- One line each, at most three per run.

More, from later runs:

- `golangci-lint` (v2) and `govulncheck` are on `claude-agent-go`, in
  `/usr/local/bin`. If you ever have to install golangci-lint yourself,
  the module path needs `/v2`
  (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`);
  without it you get v1, which rejects the config.
- `govulncheck` may report standard-library advisories against the box's
  Go that CI's newer Go doesn't have. If `main` shows the same ones, it
  isn't your change: note it and move on.
- Don't `pkill -f <pattern>`: your own shell's command line contains the
  pattern and gets killed. Use `kill $(pgrep -x <name>)`.
- `asciinema rec -q` can write nothing when there's no terminal; record
  without `-q`.

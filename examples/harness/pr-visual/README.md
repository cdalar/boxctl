# pr-visual harness

The built-in `pr-only` harness (`cmd/harness-pr-only`: work on a branch,
open a pull request, never merge) plus what a run needs so its result can
be checked by eye and so the next run doesn't relearn what this one did.
It is the harness the Linear-issue runs on boxctl's own repositories use.

    mkdir -p ~/.boxctl/harness && cp -r examples/harness/pr-visual ~/.boxctl/harness/
    boxctl claude --harness pr-visual --goal "..."

Name the copy `default` instead to have every run get it without the flag.
`README.md` is ignored by boxctl; `CLAUDE.md`, `managed-settings.json` and
`commands/` are what reach the box.

## What CLAUDE.md asks of a run

| Section | What it is for |
| --- | --- |
| Rules for this box, Delivering work, Don't | `pr-only`'s rules, unchanged |
| Visual evidence | Screenshots and a video for a browser change, a terminal recording for a backend one, in `/root/evidence/` on the box (never committed), with a README saying what was mocked |
| What earlier runs learned | Facts about the box image that cost earlier runs time |
| Before you open the pull request | Run the checks CI runs, then watch the pull request's checks |
| Report what slowed you down | A "Setup friction" list at the end of the evidence README |
| Leave what you learned about the repository in its AGENTS.md | At most three verified lines per run, in the same pull request |

## Who keeps it current

This file in git is the source; `~/.boxctl/harness/pr-visual` is a copy.
Change it here, by pull request, and copy it again.

The two memory sections work as a loop. Each run reports its setup
friction; whoever manages the runs reads those reports between waves and
moves anything new into "What earlier runs learned". A note there should
be deleted once the image fixes it -- the evidence tools, for one, are
baked into `claude-agent` since boxctl-images#12, and the notes about
installing them go when hosts have that image. Facts about one
repository don't belong here: runs write those into that repository's
`AGENTS.md` themselves.

Collecting the evidence is the manager's job: the files stay on the box
(`boxctl ssh <box> -- 'tar -C /root/evidence -c . | base64 -w0'`, in
chunks for anything large), and the box is kept until its pull request is
dealt with.

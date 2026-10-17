# pr-only harness

Starter harness for `boxctl claude --harness`: Claude may do anything on the
box, but delivers work as a pull request and never merges it.

    mkdir -p ~/.boxctl/harness && cp -r cmd/harness-pr-only ~/.boxctl/harness/pr-only
    boxctl claude --harness pr-only

`README.md` is ignored by boxctl; only `CLAUDE.md`, `managed-settings.json`
and `agents/ skills/ commands/` are copied. The deny rules are a guardrail
(Claude is root on the box), and they match command text, not intent: a
bare `git push` from a checkout of `main` with an upstream set isn't in
the list -- `CLAUDE.md`'s "never work on the default branch" is what
covers that. So also use a one-repo fine-grained token (`--github store`)
and branch protection on `main`.

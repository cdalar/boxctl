# pr-only harness

Starter harness for `boxctl claude --harness`: Claude may do anything on the
box, but delivers work as a pull request and never merges it.

    mkdir -p ~/.boxctl/harness && cp -r cmd/harness-pr-only ~/.boxctl/harness/pr-only
    boxctl claude --harness pr-only

`README.md` is ignored by boxctl; only `CLAUDE.md`, `managed-settings.json`
and `agents/ skills/ commands/` are copied. The deny rules are a guardrail
(Claude is root on the box): also use a one-repo fine-grained token
(`--github store`) and branch protection on `main`.

# OpenAI Agents API on a boxctl box

Runs an [OpenAI Agents API](https://developers.openai.com/api/docs/guides/agents-api/environments/self-hosted)
session whose environment is a boxctl box. OpenAI runs the agent; the
box runs `codex exec-server`, which dials **out** to OpenAI to receive
shell commands and file edits and send back results. The box needs no
inbound port, and your application's API key never enters it.

```
your machine ──create session, send task──▶ OpenAI Agents API
     │                                              ▲
     └──boxctl: create box, install codex──▶ box ───┘ outbound WebSocket
```

## What it does

1. Creates a box and installs Node and the Codex CLI (`@openai/codex@alpha`).
2. Writes the environment key into the box (see [Keys](#keys)).
3. Creates a session with `environment.type: "self_hosted"` and
   `/workspace` as its working directory.
4. Starts `codex exec-server` in the box with the session's environment
   ID and remote URL.
5. Sends one task, streams the agent's answer, then lists `/workspace`
   from the box itself as proof the work happened there.
6. Deletes the session and destroys the box (skip with `-keep`).

## Keys

- `OPENAI_API_KEY`: your application key, needing `api.agents.read`,
  `api.agents.write` and `api.responses.write`. It stays on your machine.
- `OPENAI_EXECUTOR_API_KEY`: an **environment key**. Create it on the
  [Agents tab](https://platform.openai.com/agents?tab=environments&environment_view=keys)
  in the same organization and project as `OPENAI_API_KEY`, with every
  other permission set to None. It can only connect environments; it is
  the only credential the box gets.

The example types the environment key into a silent `read` over the
box's terminal, which saves it to `/root/.codex-env` (mode 600). The
executor runs as a transient systemd unit that loads that file as its
`EnvironmentFile`. So the key never goes on a command line, where
`boxctl exec` commands would expose it in the host's process list while
they run. Code the agent runs can still read the key; that's inherent
to the self-hosted model, and why the key is restricted.

## Run

```bash
boxctl login   # once
export OPENAI_API_KEY=...
export OPENAI_EXECUTOR_API_KEY=...
cd examples/openai-agents
go run . -task "Create fib.py that prints the first 10 Fibonacci numbers, run it, and report its exact output."
```

Flags: `-name`, `-size` (`boxctl sizes`), `-image` (`boxctl images`),
`-model` (default `gpt-6-astra`), `-task`, `-keep`.

If the environment never connects, the example prints the executor
unit's status and the tail of `/root/exec-server.log` from the box. With
`-keep`, inspect them yourself with
`boxctl ssh <name> -- systemctl status codex-exec-server` and
`boxctl ssh <name> -- cat /root/exec-server.log`.

## Not covered yet

This is milestone 1 of a larger plan: one box, one session, one task.
Not yet handled: reusing a box across sessions, reconnecting after a
lost connection, pausing an idle box and resuming it for the next
turn, and webhook-driven startup.

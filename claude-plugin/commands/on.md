---
description: Run Bash commands in a boxctl microVM (default: one box for this session)
argument-hint: "[--mode session|project|exec] [box-name] [--size small|medium|large] [--image name]"
allowed-tools: Bash
---

Route Bash commands to boxctl. Modes:

- `session` (default): one box for this Claude Code session, destroyed when the session ends.
- `project`: one box for this project directory, kept across sessions.
- `exec`: a fresh, disposable box for every command; nothing persists between commands.

Run exactly this, with a timeout of 600000 ms (creating a box can take a few minutes):

```
CLAUDE_SESSION_ID="${CLAUDE_SESSION_ID}" CLAUDE_PROJECT_DIR="${CLAUDE_PROJECT_DIR}" "${CLAUDE_PLUGIN_ROOT}/bin/boxctl-claude" on $ARGUMENTS
```

Report the result in one or two sentences. If it fails because boxctl isn't
logged in, tell the user to run `! boxctl login`.

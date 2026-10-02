---
description: Run this project's Bash commands in a boxctl microVM
argument-hint: "[box-name] [--size small|medium|large] [--image name]"
allowed-tools: Bash
---

Route this project's Bash commands to a boxctl box. Run exactly this, with a
timeout of 600000 ms (creating a box can take a few minutes):

```
CLAUDE_PROJECT_DIR="${CLAUDE_PROJECT_DIR}" "${CLAUDE_PLUGIN_ROOT}/bin/boxctl-claude" on $ARGUMENTS
```

Report the result in one or two sentences. If it fails because boxctl isn't
logged in, tell the user to run `! boxctl login`.

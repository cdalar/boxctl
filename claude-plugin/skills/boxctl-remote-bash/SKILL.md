---
name: boxctl-remote-bash
description: How Bash behaves while the boxctl plugin routes commands to a microVM. Use when a Bash command's text was rewritten to `boxctl-claude run ...`, when a command's output doesn't match local files, or before working with files while remote bash is on.
---

While remote bash is on (`/boxctl:on`), every Bash command runs as root in a
Firecracker microVM ("box") on boxctl.io, not on the user's machine. A hook
rewrites the command to `boxctl-claude run <timeout> <cwd> <base64>`; that is
expected, not an error.

What's different:

- **Files are not synced.** Read, Edit and Write still act on local files;
  Bash sees the box's filesystem. The box starts with an empty directory at
  the same absolute path as the local project. To get the project there,
  `git clone` it in Bash (public repos, or ones the box has credentials for),
  or write the files you need with a heredoc.
- **The working directory carries over** between commands (kept on the box),
  but environment variables and shell functions do not: each command is a
  fresh non-interactive shell.
- **No stdin, no TTY**, and a hard **5-minute limit** per command. Split long
  builds into steps, or start them with `nohup ... &` and poll a log file.
- `run_in_background` still works -- it backgrounds the local wrapper.
- A paused box is resumed automatically on the next command.

To run one command locally anyway, start it with a `# local` comment line:

```
# local
git push origin HEAD
```

Commands starting with `boxctl ` always run locally too, since they drive
the box from here. Use `/boxctl:off` to go back to local bash entirely.

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

# Browser login for `boxctl login`

Status: proposal. Touches three repos (`boxctl-vms`, `boxctl-web`, this
one); nothing here is implemented yet.

## Today

`boxctl login` asks for a token at a hidden prompt (or takes it from
stdin or `BOXCTL_TOKEN`). The user has to open
`boxctl.io/dashboard/tokens` themselves, click "create", copy the token,
switch back and paste it. Four manual steps, and the token passes
through the clipboard.

## What Claude Code and gh do

Observed on a box over ssh (no browser, no localhost reachable), which
is exactly where `boxctl` is often run.

**Claude Code** (`claude auth login`, 2.1.x) prints:

```
Opening browser to sign in…
If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&...&code_challenge_method=S256&...
Paste code here if prompted >
```

It is OAuth 2 authorization code with PKCE. On a desktop the redirect
lands on a localhost callback server the CLI started, so the login
completes with no typing. Headless (`code=true`, `redirect_uri` on
`platform.claude.com/oauth/code/callback`) the hosted page shows a code
the user copies back into the terminal. `claude auth status` prints
JSON (`loggedIn`, `authMethod`, ...) and exits 1 when logged out.
`claude setup-token` prints a long-lived token for CI and saves
nothing; `CLAUDE_CODE_OAUTH_TOKEN` carries it. Environment credentials
outrank the stored login.

**gh** (`gh auth login --web`) is the OAuth device flow:

```
! First copy your one-time code: 5DC1-2101
Press Enter to open github.com in your browser...
```

The user types the code on `github.com/login/device`, approves, and the
CLI, which has been polling GitHub, picks the token up. Nothing comes
back to the machine, so it works over ssh and in containers.

**Which shape fits boxctl.** The localhost callback is the nicest on a
laptop and useless on a box, in CI, or in a container. The paste-a-code
fallback works everywhere but is two manual steps. The device flow
works everywhere with one click, and we can do better than gh: the URL
can carry the code, so the user has nothing to type at all. They sign
in (or are already signed in), check the code on the page matches the
terminal, and click Approve.

## Proposed flow

```
$ boxctl login
Opening https://boxctl.io/cli/login?code=WDKP-7H3Q in your browser…
If it didn't open, visit that URL. The page must show the code WDKP-7H3Q.
⠋ Waiting for you to approve it…
✔ Logged in as cemal@example.com (token "cli: cemal-mbp", listed at https://boxctl.io/dashboard/tokens)
```

- `--no-browser` only prints the URL (for ssh sessions where the browser
  is on another machine; `xdg-open` is missing on a box anyway, so the
  CLI falls back to this by itself when opening fails).
- Piped stdin and `BOXCTL_TOKEN` keep working exactly as now, for
  scripts: `boxctl login < token.txt`, `BOXCTL_TOKEN=... boxctl ls`.
  `--paste` forces today's hidden prompt on a terminal.
- Ctrl-C cancels; the pending login expires on the server after 10
  minutes either way.
- An older server without the endpoints (404 on the start call) makes
  the CLI fall back to the paste prompt with a one-line note, so a new
  CLI against an old self-hosted install still logs in.
- Optional, later: `boxctl whoami` (email, token label, API URL; exit 1
  when logged out, `-o json`), the equivalent of `claude auth status`.

The token's label is `cli: <hostname>`, so the dashboard's token list
says where each token lives, and revoking one from there logs that
machine out.

## Server pieces

The two servers split the way they do for everything else: boxctl-web
knows who the user is (NextAuth session, `session.userId`); boxctl-vms
owns tokens (`internal/auth/tokens.go`'s `TokenStore`) and is what the
CLI talks to. So the pending login lives in boxctl-vms, the approval
click in boxctl-web.

### boxctl-vms

A `LoginStore` next to `TicketStore` in `internal/auth`: in-memory,
single use, 10-minute TTL, swept on access. Each record: `id` (32 random
bytes, hex: the CLI's poll key), `user_code` (8 chars from a 32-symbol
alphabet, `XXXX-XXXX`: what the human sees), `label`, `created`,
`status`, and after approval the minted plaintext token, held only until
the CLI collects it.

- `POST /api/cli-login` (no auth, rate-limited per IP): body
  `{"label": "cli: cemal-mbp"}` → `{"id", "user_code", "verify_url",
  "expires_in": 600, "interval": 2}`. `verify_url` is
  `<dashboard>/cli/login?code=<user_code>`, so the CLI never hard-codes
  the dashboard's path.
- `GET /api/cli-login/{id}` (no auth; `id` is the secret): `{"status":
  "pending" | "approved" | "denied" | "expired"}`, plus `"token"` on
  `approved`, after which the record is deleted. Polling the user code
  instead of the id must not work: anyone who glimpses the code on a
  screen must not be able to collect the token.
- `GET /internal/cli-login/{user_code}` (dashboard token): the label, so
  the approval page can say "boxctl on cemal-mbp".
- `POST /internal/cli-login/{user_code}/approve` (dashboard token):
  `{"owner_prefix"}` → `TokenStore.Mint(owner_prefix, label)`, attached
  to the record. `.../deny` marks it denied.

`id` and `user_code` are separate secrets on purpose: the code is short
enough to read off a screen and compare, the id is what unlocks the
token.

### boxctl-web

- `app/cli/login/page.tsx`: `?code=XXXX-XXXX`. Without a session,
  `redirect("/signin?callbackUrl=/cli/login?code=...")`, the same as
  `app/dashboard/tokens/page.tsx`. With one: "**boxctl on cemal-mbp**
  wants to manage your boxes as you. Make sure your terminal shows the
  code **WDKP-7H3Q**.", Approve / Deny. Approved: "Done, you can close
  this tab." Expired or unknown code: say so, link to the tokens page.
- `app/api/cli-login/route.ts`: `POST {code, approve: bool}` with the
  same `session.userId` gate as `app/api/tokens/route.ts`, calling a new
  `vmsController.approveCliLogin(userId, code)` that posts
  `ownerPrefix(userId)` to boxctl-vms's internal approve route.
- `app/dashboard/tokens/page.tsx`'s copy changes from "paste the token
  at the prompt" to "run `boxctl login` and approve it here".

### boxctl (this repo)

- `internal/client`: `StartLogin(label)` and `PollLogin(id)` against
  the two public routes. Unauthenticated: the client is built with an
  empty token for them.
- `cmd/login.go`: on a terminal, the flow above through `cmd/ui.go`'s
  spinner; `--no-browser`, `--paste`. Opening the browser is
  `xdg-open`/`open`/`rundll32 url.dll,FileProtocolHandler` by OS, a
  dozen lines, no dependency; if the command is missing or fails, print
  the URL and carry on polling. Save with `config.Save` as now, and
  verify with one `List` call as now.
- Label: `"cli: " + os.Hostname()`.
- README: the quick start's `boxctl login` comment, the "How auth works"
  section, and the dashboard page's wording all change together.

## Security notes

- The device-flow risk, same as gh's: a phishing link carrying an
  attacker's code. Showing the code and the hostname on the approval
  page, and telling the user to compare it with their terminal, is the
  standard mitigation; a user who approves a code their terminal never
  showed hands a token to whoever started that login. The page should
  say that plainly.
- 40 bits of user code, 10-minute life, single use, and a rate limit on
  `POST /api/cli-login` keep guessing impractical; the poll key is the
  real secret.
- The plaintext token exists on the server only between approval and
  the next poll (seconds), then only in `~/.boxctl/config.json` (0600).
  It never passes through the browser's clipboard or the terminal's
  scrollback, which is better than today.

## Rollout

1. boxctl-vms: `LoginStore` and the four routes. Nothing calls them yet.
2. boxctl-web: the page, the API route, the tokens page wording.
3. boxctl: the new default, with the fallbacks above. Old CLIs keep
   working, since the paste path and `BOXCTL_TOKEN` stay.

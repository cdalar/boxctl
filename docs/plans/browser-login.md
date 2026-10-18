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

**Claude Code** (`claude auth login`, 2.1.x) is OAuth 2 authorization
code with PKCE. On a laptop it starts a localhost callback server,
opens the browser, and the consent page's redirect lands on that
server: sign in, click Authorize, and the terminal says "Login
successful" with nothing typed. Headless (over ssh, in a container) the
callback is unreachable, so it prints the URL with `code=true` and the
hosted page shows a code the user pastes back:

```
Opening browser to sign in…
If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&...
Paste code here if prompted >
```

`claude auth status` prints JSON (`loggedIn`, `authMethod`, ...) and
exits 1 when logged out. `claude setup-token` prints a long-lived token
for CI and saves nothing; `CLAUDE_CODE_OAUTH_TOKEN` carries it.
Environment credentials outrank the stored login.

**gh** (`gh auth login --web`) is the OAuth device flow:

```
! First copy your one-time code: 5DC1-2101
Press Enter to open github.com in your browser...
```

The user types the code on `github.com/login/device`, approves, and the
CLI, which has been polling GitHub, picks the token up.

## What boxctl should do

The user runs `boxctl` on their own computer, where the browser is. So
the experience to match is Claude Code's laptop case: run `login`, the
browser opens, one click, the terminal is logged in. The same flow has
to degrade to something usable from an ssh session or a CI job without
a second code path, and `BOXCTL_TOKEN` and piped stdin stay for scripts.

### Laptop

```
$ boxctl login
Opening your browser to approve this computer…
⠋ Waiting for you to click Approve…
✔ Logged in as you@example.com (token "cli: your-mac", listed at https://boxctl.io/dashboard/tokens)
```

The browser shows (after the usual sign-in if there's no session):

> **Allow boxctl on your-mac to manage your boxes?**
> Only approve this if you just ran `boxctl login` on that computer.
> Code WDKP-7H3Q
> [Approve] [Deny]

and after Approve: "Done. You can close this tab and go back to your
terminal." The terminal finishes within a second. Nothing is typed, and
the token never passes through the clipboard or the scrollback.

### No browser on this machine

If opening the browser fails (no `xdg-open`, ssh, container) or
`--no-browser` is given, the CLI prints the URL and the code instead:

```
Open this on any computer: https://boxctl.io/cli/login?code=WDKP-7H3Q
The page must show the code WDKP-7H3Q.
⠋ Waiting for you to click Approve…
```

Same page, same click; the code line is there so the user can check
the page they opened is their own login and not a link someone sent
them. The CLI side is identical to the laptop case.

### Scripts

Unchanged: `BOXCTL_TOKEN=... boxctl ls` needs no login at all,
`boxctl login < token.txt` saves a token from stdin, and `--paste`
gives today's hidden prompt on a terminal. Tokens for those still come
from the dashboard's tokens page.

### Why polling rather than a localhost callback

Claude Code gets its instant finish from a localhost listener the
browser redirects to. For boxctl the CLI collecting the token itself
(polling every second with a key only it holds) gives the same
experience on a laptop, finishing within the poll interval, and the
*same* code handles ssh and CI, where a localhost redirect ends in a
browser error page. One flow, no listener, no port to pick, no
mixed-content tricks to get an https page to reach `127.0.0.1`. If the
one-second wait ever matters, a callback can be added later as a pure
speed-up: the approval page would navigate to
`http://127.0.0.1:<port>/done` when the CLI advertised one, and the CLI
would collect the token the same way the moment it's hit.

### Later

`boxctl whoami`: email, token label, API URL; exit 1 when logged out,
`-o json`. The equivalent of `claude auth status`.

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
  `{"label": "cli: your-mac"}` → `{"id", "user_code", "verify_url",
  "expires_in": 600, "interval": 1}`. `verify_url` is
  `<dashboard>/cli/login?code=<user_code>`, so the CLI never hard-codes
  the dashboard's path.
- `GET /api/cli-login/{id}` (no auth; `id` is the secret): `{"status":
  "pending" | "approved" | "denied" | "expired"}`, plus `"token"` on
  `approved`, after which the record is deleted. Polling by user code
  must not work: anyone who glimpses the code on a screen must not be
  able to collect the token.
- `GET /internal/cli-login/{user_code}` (dashboard token): the label, so
  the approval page can say "boxctl on your-mac".
- `POST /internal/cli-login/{user_code}/approve` (dashboard token):
  `{"owner_prefix"}` → `TokenStore.Mint(owner_prefix, label)`, attached
  to the record. `.../deny` marks it denied.

`id` and `user_code` are separate secrets on purpose: the code is short
enough to read off a screen and compare, the id is what unlocks the
token.

### boxctl-web

- `app/cli/login/page.tsx`: `?code=XXXX-XXXX`. Without a session,
  `redirect("/signin?callbackUrl=/cli/login?code=...")`, the same as
  `app/dashboard/tokens/page.tsx`. With one, the approval page above.
  Expired or unknown code: say so, link to the tokens page.
- `app/api/cli-login/route.ts`: `POST {code, approve: bool}` with the
  same `session.userId` gate as `app/api/tokens/route.ts`, calling a new
  `vmsController.approveCliLogin(userId, code)` that posts
  `ownerPrefix(userId)` to boxctl-vms's internal approve route.
- `app/dashboard/tokens/page.tsx`'s copy changes from "paste the token
  at the prompt" to "run `boxctl login` and approve it here"; the
  create-a-token button stays, for scripts.

### boxctl (this repo)

- `internal/client`: `StartLogin(label)` and `PollLogin(id)` against
  the two public routes. Unauthenticated: the client is built with an
  empty token for them.
- `cmd/login.go`: the flow above through `cmd/ui.go`'s spinner;
  `--no-browser`, `--paste`. Opening the browser is
  `open`/`xdg-open`/`rundll32 url.dll,FileProtocolHandler` by OS, a
  dozen lines, no dependency; if it's missing or fails, print the URL
  and the code and keep polling. Ctrl-C cancels. Save with
  `config.Save` as now, and verify with one `List` call as now.
- Against an older server without the endpoints (404 on the start
  call), fall back to the `--paste` prompt with a one-line note, so a
  new CLI against an old self-hosted install still logs in.
- Label: `"cli: " + os.Hostname()`, so the dashboard's token list says
  where each token lives, and revoking one there logs that machine out.
- README: the quick start's `boxctl login` comment, the "How auth works"
  section, and the dashboard page's wording all change together.

## Security notes

- The risk every browser-approval flow has, Claude Code's and gh's
  included: a link someone sends you that carries *their* login's code.
  Approving it hands them a token. The page's wording ("only approve
  this if you just ran `boxctl login` on that computer"), the hostname,
  and the code to compare are the mitigation. A localhost callback
  would not remove this risk either: the token goes to whoever holds
  the poll key, and that's whoever started the login.
- 40 bits of user code, 10-minute life, single use, and a rate limit on
  `POST /api/cli-login` keep guessing impractical; the poll key is the
  real secret.
- The plaintext token exists on the server only between approval and
  the next poll (a second), then only in `~/.boxctl/config.json`
  (0600).

## Rollout

1. boxctl-vms: `LoginStore` and the four routes. Nothing calls them yet.
2. boxctl-web: the page, the API route, the tokens page wording.
3. boxctl: the new default, with the fallbacks above. Old CLIs keep
   working, since the paste path and `BOXCTL_TOKEN` stay.

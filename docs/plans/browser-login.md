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
`boxctl login` does what Claude Code does there: a localhost callback.
The CLI listens on a loopback port, opens the browser, the user clicks
Approve, the page redirects to that port, and the terminal is logged in
at once. Headless, where no redirect can reach the CLI, it prints the
URL and collects the token by polling instead (better than Claude's
paste-a-code: nothing to type). `BOXCTL_TOKEN` and piped stdin stay for
scripts.

### Laptop

```
$ boxctl login
Opening your browser to approve this computer…
If it didn't open, visit: https://boxctl.io/cli/login?code=WDKP-7H3Q
⠋ Waiting for you to click Approve…
✔ Logged in as you@example.com (token "cli: your-mac", listed at https://boxctl.io/dashboard/tokens)
```

Under the hood:

1. The CLI picks a random `state`, listens on `127.0.0.1:0` (a free
   port, loopback only), and starts a login on boxctl-vms with its
   label, `redirect_uri` (`http://127.0.0.1:<port>/callback`) and
   `state`. It gets back a poll key `id` and a human code `user_code`.
2. It opens `verify_url` in the browser (`open` / `xdg-open` /
   `rundll32 url.dll,FileProtocolHandler`, no dependency) and prints
   the URL too, as Claude does.
3. The browser shows (after the usual sign-in if there's no session):

   > **Allow boxctl on your-mac to manage your boxes?**
   > Only approve this if you just ran `boxctl login` on that computer.
   > Code WDKP-7H3Q
   > [Approve] [Deny]

4. Approve mints the token server-side and the page navigates to
   `redirect_uri?state=<state>`. The callback carries no secret.
5. The CLI's listener checks `state`, collects the token with its `id`
   (`GET /api/cli-login/{id}`), saves it, and answers the browser with
   a redirect to `https://boxctl.io/cli/login/done` ("You're logged in.
   You can close this tab."), so no HTML lives in the Go binary. Then
   it verifies the token with one `List` call, as `login` does today,
   and prints the ✔ line.

While waiting for the callback the CLI also polls `GET
/api/cli-login/{id}` every 2 seconds as a backstop: if the user opened
the printed URL on another device, the redirect to `127.0.0.1` fails
there, but the terminal still logs in. Ctrl-C cancels; the pending
login expires on the server after 10 minutes either way.

### No browser on this machine

With `--no-browser`, or when opening the browser fails (no `xdg-open`,
a container) or `SSH_CONNECTION` is set, the CLI starts no listener
and sends no `redirect_uri`:

```
Open this on any computer: https://boxctl.io/cli/login?code=WDKP-7H3Q
The page must show the code WDKP-7H3Q.
⠋ Waiting for you to click Approve…
```

Same page, same click. Without a `redirect_uri` the page ends with
"Done. Go back to your terminal." and the CLI collects the token by
polling. The code line is there so the user can check that the page
they opened is their own login and not a link someone sent them.

### Scripts

Unchanged: `BOXCTL_TOKEN=... boxctl ls` needs no login at all,
`boxctl login < token.txt` saves a token from stdin, and `--paste`
gives today's hidden prompt on a terminal. Tokens for those still come
from the dashboard's tokens page.

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
alphabet, `XXXX-XXXX`: what the human sees), `label`, `redirect_uri`
and `state` (both optional), `created`, `status`, and after approval
the minted plaintext token, held only until the CLI collects it.

- `POST /api/cli-login` (no auth, rate-limited per IP): body
  `{"label": "cli: your-mac", "redirect_uri": "http://127.0.0.1:51823/callback",
  "state": "..."}` → `{"id", "user_code", "verify_url", "expires_in":
  600, "interval": 2}`. `redirect_uri` is accepted only as
  `http://127.0.0.1:<port>/callback` or the `localhost` form, so the
  approval page can never be made to redirect anywhere else.
  `verify_url` is `<dashboard>/cli/login?code=<user_code>`, so the CLI
  never hard-codes the dashboard's path.
- `GET /api/cli-login/{id}` (no auth; `id` is the secret): `{"status":
  "pending" | "approved" | "denied" | "expired"}`, plus `"token"` on
  `approved` (with an optional `"email"`, who approved it, so the CLI can
  say "Logged in as you@example.com"), after which the record is
  deleted. Polling by user code must not work: anyone who glimpses the
  code on a screen must not be able to collect the token.
- `GET /internal/cli-login/{user_code}` (dashboard token): the label,
  `redirect_uri` and `state`, so the approval page can name the machine
  and know where to send the browser afterwards.
- `POST /internal/cli-login/{user_code}/approve` (dashboard token):
  `{"owner_prefix", "email"}` → `TokenStore.Mint(owner_prefix, label)`,
  attached to the record with the email. `.../deny` marks it denied.

`id` and `user_code` are separate secrets on purpose: the code is short
enough to read off a screen and compare, the id is what unlocks the
token.

### boxctl-web

- `app/cli/login/page.tsx`: `?code=XXXX-XXXX`. Without a session,
  `redirect("/signin?callbackUrl=/cli/login?code=...")`, the same as
  `app/dashboard/tokens/page.tsx`. With one, the approval page above.
  After a successful Approve: navigate to `redirect_uri?state=...` when
  the record has one, else show "Done. Go back to your terminal."
  Expired or unknown code: say so, link to the tokens page.
- `app/cli/login/done/page.tsx`: "You're logged in. You can close this
  tab." The CLI's callback redirects here.
- `app/api/cli-login/route.ts`: `POST {code, approve: bool}` with the
  same `session.userId` gate as `app/api/tokens/route.ts`, calling a new
  `vmsController.approveCliLogin(userId, code)` that posts
  `ownerPrefix(userId)` to boxctl-vms's internal approve route and
  returns the record's `redirect_uri` and `state` for the navigation.
- `app/dashboard/tokens/page.tsx`'s copy changes from "paste the token
  at the prompt" to "run `boxctl login` and approve it here"; the
  create-a-token button stays, for scripts.

### boxctl (this repo)

- `internal/client`: `StartLogin(label, redirectURI, state)` and
  `PollLogin(id)` against the two public routes. Unauthenticated: the
  client is built with an empty token for them.
- `cmd/login.go`: the flow above through `cmd/ui.go`'s spinner. The
  callback server is `net/http` on a `net.Listen("tcp", "127.0.0.1:0")`
  listener, one handler: wrong or missing `state` → 400 and keep
  waiting; right `state` → collect, redirect the browser to the done
  page, stop. `--no-browser`, `--paste`. Ctrl-C cancels. Save with
  `config.Save` as now.
- Against an older server without the endpoints (404 on the start
  call), fall back to the `--paste` prompt with a one-line note, so a
  new CLI against an old self-hosted install still logs in.
- Label: `"cli: " + os.Hostname()`, so the dashboard's token list says
  where each token lives, and revoking one there logs that machine out.
- README: the quick start's `boxctl login` comment, the "How auth works"
  section, and the dashboard page's wording all change together.

## Security notes

- The callback carries only `state`; the token is collected with the
  poll key, which never leaves the CLI. So a request from another local
  process to the callback port can't log it in, and the token is never
  in a URL, a browser history, or the clipboard. The listener binds to
  loopback only.
- The server accepts only loopback `redirect_uri`s, so the approval
  page can't be turned into an open redirect.
- The risk every browser-approval flow has, Claude Code's and gh's
  included: a link someone sends you that carries *their* login's code.
  Approving it hands them a token. The page's wording ("only approve
  this if you just ran `boxctl login` on that computer"), the hostname,
  and the code to compare are the mitigation.
- 40 bits of user code, 10-minute life, single use, and a rate limit on
  `POST /api/cli-login` keep guessing impractical; the poll key is the
  real secret.
- The plaintext token exists on the server only between approval and
  collection (milliseconds with the callback), then only in
  `~/.boxctl/config.json` (0600).

## Rollout

1. boxctl-vms: `LoginStore` and the four routes. Nothing calls them yet.
2. boxctl-web: the pages, the API route, the tokens page wording.
3. boxctl: the new default, with the fallbacks above. Old CLIs keep
   working, since the paste path and `BOXCTL_TOKEN` stay.

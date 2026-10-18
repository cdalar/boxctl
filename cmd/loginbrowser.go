package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

// browserLogin is `boxctl login` without a pasted token
// (docs/plans/browser-login.md): start a login on the server, open its
// approval page, and collect the token once the user clicks Approve.
// On this computer the page redirects to a loopback callback the CLI
// listens on, so the terminal logs in the moment the user clicks; the
// CLI also polls as a backstop, and polling alone is the whole
// mechanism when there's no callback (callback false: --no-browser,
// ssh, no opener), where the user opens the printed URL anywhere.
type browserLogin struct {
	api      *client.Client
	label    string
	callback bool
	// open opens a URL in the browser; nil never opens one and prints it
	// instead. Tests substitute it for the real opener.
	open func(url string) error
}

// loginCallbackPath is where the approval page sends the browser; it
// carries only the state nonce, never the token.
const loginCallbackPath = "/callback"

func (b *browserLogin) run(ctx context.Context) (*client.LoginStatus, error) {
	var (
		redirectURI, state string
		arrived            chan struct{}
		srv                *http.Server
		doneURL            string
	)
	if b.callback {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("listening for the browser's callback: %w", err)
		}
		state = nonce()
		redirectURI = "http://" + ln.Addr().String() + loginCallbackPath
		arrived = make(chan struct{}, 1)
		srv = &http.Server{Handler: callbackHandler(state, &doneURL, arrived), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		}()
	}

	start, err := b.api.StartLogin(ctx, b.label, redirectURI, state)
	if err != nil {
		return nil, err
	}
	doneURL = loginDoneURL(start.VerifyURL)

	opened := false
	if b.open != nil {
		if err := b.open(start.VerifyURL); err == nil {
			opened = true
			say("Opening your browser to approve this computer…")
			sayNote("If it didn't open, visit: %s", start.VerifyURL)
		}
	}
	if !opened {
		say("Open this on any computer: " + start.VerifyURL)
		sayNote("The page must show the code %s.", bold(start.UserCode))
	}

	interval := time.Duration(start.Interval) * time.Second
	if interval < time.Second {
		interval = 2 * time.Second
	}
	expires := time.Duration(start.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	deadline := time.NewTimer(expires)
	defer deadline.Stop()
	tick := time.NewTicker(interval)
	defer tick.Stop()

	sp := startSpinner("Waiting for you to click Approve…")
	fail := func(msg string) { sp.end(failMark() + " " + msg) }
	for {
		select {
		case <-ctx.Done():
			fail("Login cancelled.")
			return nil, errors.New("login cancelled")
		case <-deadline.C:
			fail("Nobody approved it in time.")
			return nil, errors.New("the login expired before it was approved; run `boxctl login` again")
		case <-arrived:
			// The browser came back, so the server has the token; a
			// poll that still says pending is the approve and the
			// redirect racing, and the next tick resolves it.
		case <-tick.C:
		}
		st, err := b.api.PollLogin(ctx, start.ID)
		if err != nil {
			fail("Could not reach the server.")
			return nil, err
		}
		switch st.Status {
		case "pending":
			continue
		case "approved":
			if st.Token == "" {
				fail("The server approved the login but sent no token.")
				return nil, errors.New("the server approved the login but sent no token")
			}
			sp.end("")
			return st, nil
		case "denied":
			fail("You denied it in the browser.")
			return nil, errors.New("login denied in the browser")
		case "expired":
			fail("Nobody approved it in time.")
			return nil, errors.New("the login expired before it was approved; run `boxctl login` again")
		default:
			fail("Unexpected reply from the server.")
			return nil, fmt.Errorf("unexpected login status %q", st.Status)
		}
	}
}

// callbackHandler answers the approval page's redirect: the right state
// signals arrived once and sends the browser on to the done page; a
// wrong or missing state is refused and changes nothing, so a stray
// local request can't end the wait.
func callbackHandler(state string, doneURL *string, arrived chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != loginCallbackPath {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "this isn't the login boxctl is waiting for", http.StatusBadRequest)
			return
		}
		select {
		case arrived <- struct{}{}:
		default:
		}
		http.Redirect(w, r, *doneURL, http.StatusFound)
	})
}

// loginDoneURL is the dashboard's "you're logged in, close this tab"
// page, on the same origin as the approval page so a self-hosted
// dashboard gets its own.
func loginDoneURL(verifyURL string) string {
	u, err := url.Parse(verifyURL)
	if err != nil || u.Host == "" {
		return dashboardURL
	}
	return u.Scheme + "://" + u.Host + "/cli/login/done"
}

func nonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// browserOpener is the OS's open-a-URL command, or "" when this machine
// has none (a box, a container), in which case there's no point
// listening for a callback either.
func browserOpener() (name string, args []string) {
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		name = "xdg-open"
	}
	if _, err := exec.LookPath(name); err != nil {
		return "", nil
	}
	return name, args
}

// openBrowser hands url to the OS's opener and doesn't wait for it: the
// browser may outlive this process.
func openBrowser(url string) error {
	name, args := browserOpener()
	if name == "" {
		return errors.New("no browser opener on this machine")
	}
	c := exec.Command(name, append(args, url)...)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}

// newBrowserLogin decides how to log in from here: a callback when this
// computer can open a browser and isn't an ssh session (the browser
// would be elsewhere), otherwise the printed URL and polling.
func newBrowserLogin(api *client.Client, noBrowser bool) *browserLogin {
	host, _ := os.Hostname()
	if host == "" {
		host = "this computer"
	}
	b := &browserLogin{api: api, label: "cli: " + host}
	opener, _ := browserOpener()
	if !noBrowser && opener != "" && os.Getenv("SSH_CONNECTION") == "" {
		b.callback = true
		b.open = openBrowser
	}
	return b
}

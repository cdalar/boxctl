package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

// fakeLoginServer is boxctl-vms's two public login routes
// (docs/plans/browser-login.md), with the approval click replaced by a
// call to approve/deny from the test.
type fakeLoginServer struct {
	mu          sync.Mutex
	srv         *httptest.Server
	status      string // what the next poll answers
	redirectURI string // from the start request
	state       string
	label       string
	auth        string // Authorization header seen on the start request
	polls       int
	collected   bool // the token was handed out: the login is gone
}

func newFakeLoginServer(t *testing.T) *fakeLoginServer {
	t.Helper()
	f := &fakeLoginServer{status: "pending"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLoginServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/cli-login":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.label, f.redirectURI, f.state = body["label"], body["redirect_uri"], body["state"]
		f.auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(client.LoginStart{
			ID: "id-1", UserCode: "ABCD-1234",
			VerifyURL: f.srv.URL + "/cli/login?code=ABCD-1234",
			ExpiresIn: 600, Interval: 1,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/api/cli-login/id-1" && !f.collected:
		f.polls++
		st := client.LoginStatus{Status: f.status}
		if f.status == "approved" {
			st.Token, st.Email = "bxc_test", "you@example.com"
			f.collected = true
		}
		_ = json.NewEncoder(w).Encode(st)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeLoginServer) set(status string) {
	f.mu.Lock()
	f.status = status
	f.mu.Unlock()
}

func (f *fakeLoginServer) startReq() (redirectURI, state, label, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.redirectURI, f.state, f.label, f.auth
}

// hit is the browser following the approval page's redirect to the
// CLI's callback, without following the CLI's own redirect onward.
func hit(t *testing.T, url string) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = res.Body.Close()
	return res
}

func TestBrowserLoginCallback(t *testing.T) {
	f := newFakeLoginServer(t)
	opened := make(chan string, 1)
	b := &browserLogin{
		api: client.New(f.srv.URL, ""), label: "cli: test", callback: true,
		open: func(u string) error { opened <- u; return nil },
	}

	type result struct {
		st  *client.LoginStatus
		err error
	}
	done := make(chan result, 1)
	go func() {
		st, err := b.run(context.Background())
		done <- result{st, err}
	}()

	select {
	case u := <-opened:
		if u != f.srv.URL+"/cli/login?code=ABCD-1234" {
			t.Errorf("opened %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the browser was never opened")
	}
	redirectURI, state, label, auth := f.startReq()
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") || !strings.HasSuffix(redirectURI, "/callback") {
		t.Errorf("redirect_uri = %q", redirectURI)
	}
	if len(state) != 32 || label != "cli: test" || auth != "" {
		t.Errorf("state %q label %q auth %q", state, label, auth)
	}

	// The browser comes back with the wrong state: refused, still waiting.
	if res := hit(t, redirectURI+"?state=nope"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong state: HTTP %d", res.StatusCode)
	}
	if res := hit(t, strings.TrimSuffix(redirectURI, "/callback")+"/other?state="+state); res.StatusCode != http.StatusNotFound {
		t.Errorf("other path: HTTP %d", res.StatusCode)
	}
	select {
	case r := <-done:
		t.Fatalf("run ended on a bad callback: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	// Approve, then the real redirect: the CLI collects the token at
	// once and sends the browser to the dashboard's done page.
	f.set("approved")
	res := hit(t, redirectURI+"?state="+state)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != f.srv.URL+"/cli/login/done" {
		t.Errorf("callback answered HTTP %d, Location %q", res.StatusCode, res.Header.Get("Location"))
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("run: %v", r.err)
		}
		if r.st.Token != "bxc_test" || r.st.Email != "you@example.com" {
			t.Errorf("got %+v", r.st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run didn't finish after the callback")
	}
}

func TestBrowserLoginPollOnly(t *testing.T) {
	f := newFakeLoginServer(t)
	b := &browserLogin{api: client.New(f.srv.URL, ""), label: "cli: test"} // no callback, no opener
	go func() {
		time.Sleep(1500 * time.Millisecond) // after the first poll
		f.set("approved")
	}()
	st, err := b.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st.Token != "bxc_test" {
		t.Errorf("token %q", st.Token)
	}
	if redirectURI, state, _, _ := f.startReq(); redirectURI != "" || state != "" {
		t.Errorf("a poll-only login sent redirect_uri %q state %q", redirectURI, state)
	}
	f.mu.Lock()
	polls := f.polls
	f.mu.Unlock()
	if polls < 2 {
		t.Errorf("polled %d times, expected the pending answer first", polls)
	}
}

func TestBrowserLoginDenied(t *testing.T) {
	f := newFakeLoginServer(t)
	f.set("denied")
	b := &browserLogin{api: client.New(f.srv.URL, "")}
	if _, err := b.run(context.Background()); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserLoginCancelled(t *testing.T) {
	f := newFakeLoginServer(t)
	b := &browserLogin{api: client.New(f.srv.URL, ""), callback: true, open: func(string) error { return errors.New("no browser") }}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if _, err := b.run(ctx); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrowserLoginOldServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	b := &browserLogin{api: client.New(srv.URL, "")}
	if _, err := b.run(context.Background()); !errors.Is(err, client.ErrNoBrowserLogin) {
		t.Fatalf("err = %v, want ErrNoBrowserLogin", err)
	}
}

func TestLoginDoneURL(t *testing.T) {
	if got := loginDoneURL("https://boxctl.io/cli/login?code=X"); got != "https://boxctl.io/cli/login/done" {
		t.Errorf("got %q", got)
	}
	if got := loginDoneURL("http://localhost:3000/cli/login?code=X"); got != "http://localhost:3000/cli/login/done" {
		t.Errorf("got %q", got)
	}
	if got := loginDoneURL("::nonsense"); got != dashboardURL {
		t.Errorf("got %q", got)
	}
}

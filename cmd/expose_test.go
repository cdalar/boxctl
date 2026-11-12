package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestParseExposePort(t *testing.T) {
	for _, ok := range []string{"1", "80", "3000", "65535"} {
		if _, err := parseExposePort(ok); err != nil {
			t.Errorf("parseExposePort(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "65536", "-1", "http", "3000:80", "30 00", "all"} {
		if _, err := parseExposePort(bad); err == nil {
			t.Errorf("parseExposePort(%q) accepted", bad)
		}
	}
}

func TestExposeNote(t *testing.T) {
	cases := map[string]string{
		"active":    "0.0.0.0:3000",
		"paused":    "boxctl resume my-box",
		"suspended": "plan",
		"pending":   "boxctl expose my-box",
		"":          "boxctl expose my-box", // a status this CLI doesn't know reads as "not yet"
	}
	for status, want := range cases {
		got := exposeNote("my-box", &client.Ingress{Port: 3000, Status: status})
		if !strings.Contains(got, want) {
			t.Errorf("status %q: note %q doesn't mention %q", status, got, want)
		}
	}
}

// fakeIngress serves POST /api/vms/box/ingress, recording the body and
// answering with status/reply.
func fakeIngress(t *testing.T, status int, reply string, got *map[string]any) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "tok")
}

func TestExposeSendsCredentials(t *testing.T) {
	var got map[string]any
	c := fakeIngress(t, 200, `{"port":3000,"url":"https://x","basic_auth":true,"basic_auth_user":"demo"}`, &got)
	user := "demo"
	in, err := c.Expose(context.Background(), "box", 3000, client.ExposeOptions{BasicAuthUser: &user, BasicAuthPassword: "s3cretpass"})
	if err != nil {
		t.Fatal(err)
	}
	if got["basic_auth_user"] != "demo" || got["basic_auth_password"] != "s3cretpass" {
		t.Errorf("body = %v", got)
	}
	if !in.BasicAuth || in.BasicAuthUser != "demo" {
		t.Errorf("ingress = %+v", in)
	}
	if h := curlHint(in); !strings.Contains(h, "curl -u demo https://x") || strings.Contains(h, "s3cretpass") {
		t.Errorf("hint = %q", h)
	}
}

func TestExposeRemoveAndLeaveAlone(t *testing.T) {
	var got map[string]any
	c := fakeIngress(t, 200, `{"port":3000}`, &got)
	none := ""
	if _, err := c.Expose(context.Background(), "box", 3000, client.ExposeOptions{BasicAuthUser: &none}); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["basic_auth_user"]; !ok || v != "" {
		t.Errorf("remove body = %v", got)
	}
	if _, ok := got["basic_auth_password"]; ok {
		t.Errorf("remove sent a password: %v", got)
	}
	got = nil
	if _, err := c.Expose(context.Background(), "box", 3000, client.ExposeOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["basic_auth_user"]; ok {
		t.Errorf("plain expose sent auth fields: %v", got)
	}
}

func TestExposeAuthErrors(t *testing.T) {
	user := "demo"
	opts := client.ExposeOptions{BasicAuthUser: &user, BasicAuthPassword: "s3cretpass"}
	var got map[string]any
	_, err := fakeIngress(t, 400, "bad credentials", &got).Expose(context.Background(), "box", 1, opts)
	if err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Errorf("400: %v", err)
	}
	_, err = fakeIngress(t, 409, "no key", &got).Expose(context.Background(), "box", 1, opts)
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Errorf("409: %v", err)
	}
}

func TestExposeOptions(t *testing.T) {
	reset := func() { exposeAuthUser, exposeNoAuth, exposePasswordStdin, exposeHostHeader = "", false, false, "" }
	defer reset()
	t.Setenv(exposePasswordEnv, "")

	exposeAuthUser, exposePasswordStdin = "demo", true
	o, err := exposeOptions(strings.NewReader("longenough1\nrest"), nil)
	if err != nil || o.BasicAuthPassword != "longenough1" || *o.BasicAuthUser != "demo" {
		t.Errorf("stdin: %+v %v", o, err)
	}
	t.Setenv(exposePasswordEnv, "fromenvpass")
	if o, err = exposeOptions(strings.NewReader("stdinpass1\n"), nil); err != nil || o.BasicAuthPassword != "stdinpass1" {
		t.Errorf("stdin over env: %+v %v", o, err)
	}
	exposePasswordStdin = false
	if o, err = exposeOptions(strings.NewReader(""), nil); err != nil || o.BasicAuthPassword != "fromenvpass" {
		t.Errorf("env: %+v %v", o, err)
	}
	t.Setenv(exposePasswordEnv, "short")
	if _, err = exposeOptions(strings.NewReader(""), nil); err == nil {
		t.Error("short password accepted")
	}
	t.Setenv(exposePasswordEnv, "")
	exposePasswordStdin = false
	if _, err = exposeOptions(strings.NewReader(""), nil); err == nil || !strings.Contains(err.Error(), exposePasswordEnv) {
		t.Errorf("no source: %v", err)
	}
	exposeNoAuth = true
	if _, err = exposeOptions(nil, nil); err == nil {
		t.Error("--no-basic-auth with a user accepted")
	}
	exposeAuthUser = ""
	if o, err = exposeOptions(nil, nil); err != nil || o.BasicAuthUser == nil || *o.BasicAuthUser != "" {
		t.Errorf("remove: %+v %v", o, err)
	}
}

func TestValidateBasicAuth(t *testing.T) {
	for _, c := range []struct {
		u, p string
		ok   bool
	}{
		{"demo", "password1", true}, {"a.b_c@d+e-f", "p@ss~word!", true},
		{"", "password1", false}, {"bad user", "password1", false}, {strings.Repeat("a", 65), "password1", false},
		{"demo", "short", false}, {"demo", "has space1", false}, {"demo", "br{ace}s12", false}, {"demo", "ünïcode123", false},
	} {
		if err := client.ValidateBasicAuth(c.u, c.p); (err == nil) != c.ok {
			t.Errorf("%q/%q: %v", c.u, c.p, err)
		}
	}
}

func TestAuthText(t *testing.T) {
	if authText(client.Ingress{}) != "-" || authText(client.Ingress{BasicAuth: true, BasicAuthUser: "demo"}) != "demo" {
		t.Error("authText")
	}
}

func TestExposeTablesUnchangedWithoutAuth(t *testing.T) {
	ing := client.Ingress{Port: 3000, Status: "active", URL: "https://a.example.com"}
	var got bytes.Buffer
	if err := writeIngressTable(&got, []client.Ingress{ing}); err != nil {
		t.Fatal(err)
	}
	want := "PORT  STATUS  URL                    HOST HEADER  AGE\n3000  active  https://a.example.com  -            -\n"
	if got.String() != want {
		t.Errorf("got %q want %q", got.String(), want)
	}
	got.Reset()
	if err := writeBoxIngressTable(&got, []boxIngress{{Box: "b", Ingress: ing}}); err != nil {
		t.Fatal(err)
	}
	want = "BOX  PORT  STATUS  URL                    AGE\nb    3000  active  https://a.example.com  -\n"
	if got.String() != want {
		t.Errorf("got %q want %q", got.String(), want)
	}
}

func TestExposeTablesAuthAfterAge(t *testing.T) {
	ing := client.Ingress{Port: 3000, Status: "active", URL: "u", BasicAuth: true, BasicAuthUser: "demo"}
	var got bytes.Buffer
	_ = writeBoxIngressTable(&got, []boxIngress{{Box: "b", Ingress: ing}, {Box: "c", Ingress: client.Ingress{Port: 1, URL: "v"}}})
	lines := strings.Split(strings.TrimSpace(got.String()), "\n")
	if !strings.HasSuffix(lines[0], "AGE  AUTH") || !strings.HasSuffix(lines[1], "demo") || !strings.HasSuffix(lines[2], "-") {
		t.Errorf("unexpected table:\n%s", got.String())
	}
}

func TestReadExposePasswordOrder(t *testing.T) {
	pw, err := readExposePassword("envpassword", strings.NewReader("stdinpassword\r\n"), nil, true)
	if err != nil || pw != "stdinpassword" {
		t.Errorf("stdin should win: %q %v", pw, err)
	}
	pw, _ = readExposePassword("envpassword\r\n", nil, nil, false)
	if pw != "envpassword" {
		t.Errorf("env newline: %q", pw)
	}
	pw, _ = readExposePassword("envpassword\r\r\n", nil, nil, false)
	if pw != "envpassword\r" {
		t.Errorf("only one CRLF trimmed: %q", pw)
	}
}

func TestBasicAuthErrorsNameOneRule(t *testing.T) {
	if err := client.ValidateBasicAuthUser("bad user"); err == nil || strings.Contains(err.Error(), "password") {
		t.Errorf("user error: %v", err)
	}
	if err := client.ValidateBasicAuth("demo", "short"); err == nil || strings.Contains(err.Error(), "username") || strings.Contains(err.Error(), "{") {
		t.Errorf("password error: %v", err)
	}
}

func TestExpose400UnwrapsJSON(t *testing.T) {
	user := "demo"
	opts := client.ExposeOptions{BasicAuthUser: &user, BasicAuthPassword: "longenough"}
	_, err := fakeIngress(t, 400, `{"error": "password too weak"}`, new(map[string]any)).Expose(context.Background(), "box", 1, opts)
	if err == nil || !strings.Contains(err.Error(), "password too weak") || strings.Contains(err.Error(), `{"error"`) {
		t.Errorf("400: %v", err)
	}
}

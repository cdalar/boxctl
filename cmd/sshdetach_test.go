package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"
)

// fakeBoxAPI serves POST /api/vms/<name>/exec by running the command
// with sh in a private $HOME, so the detach scripts run for real. It
// records the timeout_seconds each call carried.
// fakeFail502 is how many of the next exec calls the fake answers with a 502.
var fakeFail502 atomic.Int32

func fakeBoxAPI(t *testing.T) (c *client.Client, timeouts *[]int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	var mu sync.Mutex
	timeouts = new([]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fakeFail502.Load() > 0 {
			fakeFail502.Add(-1)
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		var body struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout_seconds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		*timeouts = append(*timeouts, body.Timeout)
		mu.Unlock()
		var out, errb strings.Builder
		cmd := exec.Command("sh", "-c", body.Command)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := 0
		if err := cmd.Run(); err != nil {
			code = err.(*exec.ExitError).ExitCode()
		}
		_ = json.NewEncoder(w).Encode(client.ExecResult{ExitCode: code, Stdout: out.String(), Stderr: errb.String()})
	}))
	t.Cleanup(srv.Close)
	old := cfg
	cfg = &config.Config{APIURL: srv.URL, Token: "tok"}
	t.Cleanup(func() { cfg = old })
	return client.New(srv.URL, "tok"), timeouts
}

func TestValidateExecTimeout(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 30 * time.Second, 5 * time.Minute} {
		if err := client.ValidateExecTimeout(d); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	for _, d := range []time.Duration{0, -time.Second, 500 * time.Millisecond, 1500 * time.Millisecond, 20 * time.Minute} {
		err := client.ValidateExecTimeout(d)
		if err == nil || !strings.Contains(err.Error(), "5m0s") {
			t.Errorf("%s: want a refusal naming 5m0s, got %v", d, err)
		}
	}
}

// exec -T 20m is refused before any request (so before a box is created).
func TestExecRefusesLongTimeoutBeforeCreating(t *testing.T) {
	_, timeouts := fakeBoxAPI(t)
	execTimeout = 20 * time.Minute
	t.Cleanup(func() { execTimeout = 30 * time.Second })
	err := execCmd.RunE(execCmd, []string{"true"})
	if err == nil || !strings.Contains(err.Error(), "5m0s") {
		t.Fatalf("got %v", err)
	}
	if len(*timeouts) != 0 {
		t.Fatalf("API was called: %v", *timeouts)
	}
}

// A timeout passes the server's message through; when its limit differs
// from the one asked for, both are said.
func TestExecTimeoutMessagePassedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exit_code":-1,"error":"command timed out after 30s"}`))
	}))
	defer srv.Close()
	c := client.New(srv.URL, "t")
	res, err := c.Exec(context.Background(), "b", "sleep 99", 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if want := "command timed out after 30s (this client asked for 1m30s)"; res.Error != want {
		t.Fatalf("got %q", res.Error)
	}
	res, _ = c.Exec(context.Background(), "b", "sleep 99", 30*time.Second)
	if res.Error != "command timed out after 30s" {
		t.Fatalf("got %q", res.Error)
	}
}

func TestSSHDetachThenWait(t *testing.T) {
	_, timeouts := fakeBoxAPI(t)
	detachPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { detachPollInterval = 2 * time.Second })
	ctx := context.Background()

	if err := runSSHDetach(ctx, "box", "echo line1; sleep 0.3; echo line2; exit 7", io.Discard); err != nil {
		t.Fatal(err)
	}
	// The start call returned long before the command finished.
	code, err := runSSHWait(ctx, "box", "", true, io.Discard)
	if err != nil || code != 7 {
		t.Fatalf("wait: code %d err %v", code, err)
	}
	for _, to := range *timeouts {
		if to > 30 {
			t.Errorf("a call asked for %ds; detach/wait must stay short", to)
		}
	}
}

func TestSSHWaitFollowStreamsLogAndStatus(t *testing.T) {
	fakeBoxAPI(t)
	detachPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { detachPollInterval = 2 * time.Second })
	ctx := context.Background()
	if err := runSSHDetach(ctx, "box", "echo hello-log; exit 0", io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := runSSHWait(ctx, "box", "", true, &out)
	if err != nil || code != 0 || out.String() != "hello-log\n" {
		t.Fatalf("code %d err %v out %q", code, err, out.String())
	}
}

func TestSSHWaitWithNothingDetached(t *testing.T) {
	fakeBoxAPI(t)
	_, err := runSSHWait(context.Background(), "box", "", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no detached command") {
		t.Fatalf("got %v", err)
	}
}

func TestSSHFlagValidation(t *testing.T) {
	fakeBoxAPI(t)
	defer func() { sshDetach, sshWait, sshFollow, sshTimeout = false, false, false, 30*time.Second }()
	sshFollow = true
	if err := sshCmd.RunE(sshCmd, []string{"b"}); err == nil {
		t.Error("--follow alone accepted")
	}
	sshFollow, sshDetach = false, true
	if err := sshCmd.RunE(sshCmd, []string{"b"}); err == nil {
		t.Error("--detach without a command accepted")
	}
	sshDetach = true
	_ = sshCmd.Flags().Set("timeout", "1m")
	if err := sshCmd.RunE(sshCmd, []string{"b", "ls"}); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("--detach -T accepted: %v", err)
	}
	sshCmd.Flags().Lookup("timeout").Changed = false
	sshDetach, sshTimeout = false, 10*time.Minute
	if err := sshCmd.RunE(sshCmd, []string{"b", "ls"}); err == nil || !strings.Contains(err.Error(), "5m0s") {
		t.Errorf("-T 10m: %v", err)
	}
}

func fastPolls(t *testing.T) {
	detachPollInterval = 10 * time.Millisecond
	detachRetryBase, detachRetryMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		detachPollInterval = 2 * time.Second
		detachRetryBase, detachRetryMax = time.Second, 15*time.Second
	})
}

// The followed log is byte-exact: invalid UTF-8 and a multibyte
// character across the chunk boundary come through unchanged.
func TestSSHWaitFollowIsByteExact(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	ctx := context.Background()
	cmd := fmt.Sprintf(`head -c %d /dev/zero | tr '\0' a; printf '\303\251\377\376\200\342\202'; head -c 300000 /dev/zero | tr '\0' b; printf '\342\202\254end'`, detachChunk-1)
	if err := runSSHDetach(ctx, "box", cmd, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code, err := runSSHWait(ctx, "box", "", true, &out)
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v", code, err)
	}
	var want bytes.Buffer
	want.Write(bytes.Repeat([]byte("a"), detachChunk-1))
	want.WriteString("\xc3\xa9\xff\xfe\x80\xe2\x82")
	want.Write(bytes.Repeat([]byte("b"), 300000))
	want.WriteString("\xe2\x82\xacend")
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Fatalf("log differs: got %d bytes, want %d", out.Len(), want.Len())
	}
}

// A wrapper that dies without writing a status ends --wait with 251.
func TestSSHWaitLostWrapper(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	ctx := context.Background()
	if err := runSSHDetach(ctx, "box", "sleep 30", io.Discard); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(os.Getenv("HOME"), ".boxctl", "detached", "latest", "pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	_, _ = fmt.Sscan(string(data), &pid)
	// Kill the wrapper's whole group (setsid made one): no status is written.
	_ = exec.Command("sh", "-c", fmt.Sprintf("kill -9 -%d %d 2>/dev/null; kill -9 %d", pid, pid, pid)).Run()
	code, err := runSSHWait(ctx, "box", "", false, io.Discard)
	if code != exitRunLost || err == nil || !strings.Contains(err.Error(), "left no exit status") {
		t.Fatalf("code %d err %v", code, err)
	}
}

// A 502 in the middle of a wait is retried; the exit status still comes.
func TestSSHWaitSurvivesTransientFailure(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	ctx := context.Background()
	if err := runSSHDetach(ctx, "box", "sleep 0.2; echo done; exit 3", io.Discard); err != nil {
		t.Fatal(err)
	}
	fakeFail502.Store(2)
	var out bytes.Buffer
	code, err := runSSHWait(ctx, "box", "", true, &out)
	if err != nil || code != 3 || out.String() != "done\n" {
		t.Fatalf("code %d err %v out %q", code, err, out.String())
	}
}

// Failures that never end give up with the "wait failed" code.
func TestSSHWaitGivesUp(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	detachRetryFor = 60 * time.Millisecond
	t.Cleanup(func() { detachRetryFor = 5 * time.Minute })
	fakeFail502.Store(1000)
	t.Cleanup(func() { fakeFail502.Store(0) })
	code, err := runSSHWait(context.Background(), "box", "", false, io.Discard)
	if code != exitWaitFailed || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
}

// --wait <id> follows that run even after a later --detach.
func TestSSHWaitByID(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	ctx := context.Background()
	var o bytes.Buffer
	if err := runSSHDetach(ctx, "box", "sleep 0.2; exit 4", &o); err != nil {
		t.Fatal(err)
	}
	first := strings.Fields(strings.TrimPrefix(o.String(), "Started run "))[0]
	if err := runSSHDetach(ctx, "box", "exit 5", io.Discard); err != nil {
		t.Fatal(err)
	}
	if code, err := runSSHWait(ctx, "box", first, false, io.Discard); err != nil || code != 4 {
		t.Fatalf("first: code %d err %v", code, err)
	}
	if code, err := runSSHWait(ctx, "box", "", false, io.Discard); err != nil || code != 5 {
		t.Fatalf("latest: code %d err %v", code, err)
	}
	if code, err := runSSHWait(ctx, "box", "../x", false, io.Discard); err == nil || code != exitWaitFailed {
		t.Fatalf("bad id: code %d err %v", code, err)
	}
}

// Only the newest detachKeep runs stay on the box.
func TestSSHDetachPrunesOldRuns(t *testing.T) {
	fakeBoxAPI(t)
	fastPolls(t)
	ctx := context.Background()
	for i := 0; i < detachKeep+4; i++ {
		if err := runSSHDetach(ctx, "box", "true", io.Discard); err != nil {
			t.Fatal(err)
		}
		if _, err := runSSHWait(ctx, "box", "", false, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".boxctl", "detached"))
	n := 0
	for _, e := range ents {
		if e.Name() != "latest" {
			n++
		}
	}
	if n != detachKeep {
		t.Fatalf("%d runs kept, want %d", n, detachKeep)
	}
}

// --wait on a paused box says so instead of a bare 409.
func TestSSHWaitPausedBox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"name":"box","state":"paused"}]`))
			return
		}
		http.Error(w, "must be running and ready", http.StatusConflict)
	}))
	defer srv.Close()
	old := cfg
	cfg = &config.Config{APIURL: srv.URL, Token: "tok"}
	defer func() { cfg = old }()
	fastPolls(t)
	code, err := runSSHWait(context.Background(), "box", "", false, io.Discard)
	if code != exitWaitFailed || err == nil || !strings.Contains(err.Error(), "boxctl resume box") {
		t.Fatalf("code %d err %v", code, err)
	}
}

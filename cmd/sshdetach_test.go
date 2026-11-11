package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/cdalar/boxctl/internal/config"
)

// fakeBoxAPI serves POST /api/vms/<name>/exec by running the command
// with sh in a private $HOME, so the detach scripts run for real. It
// records the timeout_seconds each call carried.
func fakeBoxAPI(t *testing.T) (c *client.Client, timeouts *[]int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	var mu sync.Mutex
	timeouts = new([]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// A timeout reports the limit that was applied (the one sent).
func TestExecTimeoutMessageNamesAppliedLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exit_code":-1,"error":"command timed out after 30s"}`))
	}))
	defer srv.Close()
	res, err := client.New(srv.URL, "t").Exec(context.Background(), "b", "sleep 99", 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "command timed out after 1m30s" {
		t.Fatalf("got %q", res.Error)
	}
}

func TestSSHDetachThenWait(t *testing.T) {
	_, timeouts := fakeBoxAPI(t)
	detachPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { detachPollInterval = 2 * time.Second })
	ctx := context.Background()

	if err := runSSHDetach(ctx, "box", "echo line1; sleep 0.3; echo line2; exit 7"); err != nil {
		t.Fatal(err)
	}
	// The start call returned long before the command finished.
	code, err := runSSHWait(ctx, "box", true)
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
	if err := runSSHDetach(ctx, "box", "echo hello-log; exit 0"); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	code, err := runSSHWait(ctx, "box", true)
	os.Stdout = old
	_ = w.Close()
	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	if err != nil || code != 0 || string(buf[:n]) != "hello-log\n" {
		t.Fatalf("code %d err %v out %q", code, err, buf[:n])
	}
}

func TestSSHWaitWithNothingDetached(t *testing.T) {
	fakeBoxAPI(t)
	_, err := runSSHWait(context.Background(), "box", false)
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
	sshDetach, sshTimeout = false, 10*time.Minute
	if err := sshCmd.RunE(sshCmd, []string{"b", "ls"}); err == nil || !strings.Contains(err.Error(), "5m0s") {
		t.Errorf("-T 10m: %v", err)
	}
}

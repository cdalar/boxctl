package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// fakeSSH puts an ssh on PATH that exits with each of codes in turn, one
// per run, and then with the last.
func fakeSSH(t *testing.T, codes ...int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nn=$(cat " + dir + "/n 2>/dev/null || echo 0)\necho $((n+1)) >" + dir + "/n\ncase $n in\n"
	for i, c := range codes {
		if i == len(codes)-1 {
			script += "*) exit " + strconv.Itoa(c) + " ;;\n"
		} else {
			script += strconv.Itoa(i) + ") exit " + strconv.Itoa(c) + " ;;\n"
		}
	}
	script += "esac\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func testBox() *boxSSH { return &boxSSH{name: "claude-x", args: []string{"root@claude-x"}} }

func TestAttachTellsALostConnectionFromAnEnd(t *testing.T) {
	for code, want := range map[int]error{0: nil, 1: nil, 130: nil, 255: errConnectionLost} {
		fakeSSH(t, code)
		if err := testBox().attach(context.Background(), "tmux"); !errors.Is(err, want) {
			t.Errorf("ssh exiting %d: got %v, want %v", code, err, want)
		}
	}
}

func TestSessionAlive(t *testing.T) {
	sess := taskSession{tmux: "claude"}
	for code, want := range map[int]struct{ running, reached bool }{0: {true, true}, 1: {false, true}, 255: {false, false}} {
		fakeSSH(t, code)
		running, err := sessionAlive(context.Background(), testBox(), sess)
		if running != want.running || (err == nil) != want.reached {
			t.Errorf("ssh exiting %d: running %v, err %v", code, running, err)
		}
	}
}

func TestReattach(t *testing.T) {
	sess := taskSession{tmux: "claude"}
	// Unreachable twice, then reachable with the session running.
	fakeSSH(t, 255, 255, 0)
	attaches := 0
	err := reattach(context.Background(), testBox(), sess, func() error {
		attaches++
		if attaches == 1 {
			return errConnectionLost
		}
		return nil // detached
	})
	if err != nil || attaches != 2 {
		t.Errorf("reconnect and reattach: err %v, %d attaches", err, attaches)
	}

	// Reachable, but the session ended meanwhile: not attached again.
	fakeSSH(t, 1)
	attaches = 0
	err = reattach(context.Background(), testBox(), sess, func() error { attaches++; return errConnectionLost })
	if err != nil || attaches != 1 {
		t.Errorf("session gone: err %v, %d attaches", err, attaches)
	}

	// Never reachable: gives up when the context ends.
	fakeSSH(t, 255)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err = reattach(ctx, testBox(), sess, func() error { return errConnectionLost })
	if !errors.Is(err, errConnectionLost) {
		t.Errorf("giving up: got %v", err)
	}
}

func TestAttachArgsDetectADeadConnectionSooner(t *testing.T) {
	b := &boxSSH{name: "x", args: []string{"-o", "ServerAliveInterval=30", "root@x"}}
	got := b.attachArgs("tmux")
	if i, j := slices.Index(got, "ServerAliveInterval=10"), slices.Index(got, "ServerAliveInterval=30"); i < 0 || i > j {
		t.Fatalf("ssh takes an option's first value; the attach's must come first: %v", got)
	}
}

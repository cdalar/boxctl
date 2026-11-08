package cmd

import "testing"

func TestSSHEscape(t *testing.T) {
	for _, c := range []struct {
		in   []string // separate reads
		want string
		quit bool
	}{
		{[]string{"~."}, "", true}, // at the very start
		{[]string{"ls\r~."}, "ls\r", true},
		{[]string{"ls\r~", "."}, "ls\r", true}, // split across reads
		{[]string{"a~.b"}, "a~.b", false},      // not at a line start
		{[]string{"\r~~."}, "\r~.", false},     // ~~ is one ~
		{[]string{"\r~x"}, "\r~x", false},
		{[]string{"~\r~.rest"}, "~\r", true},
	} {
		var e sshEscape
		got, quit := "", false
		for _, r := range c.in {
			out, q := e.feed([]byte(r))
			got += string(out)
			if q {
				quit = true
				break
			}
		}
		if got != c.want || quit != c.quit {
			t.Errorf("feed(%q) = %q, %v; want %q, %v", c.in, got, quit, c.want, c.quit)
		}
	}
}

func TestTerminalWSURL(t *testing.T) {
	if got, want := terminalWSURL("https://api.x", "u1-box", "t k", 0), "wss://api.x/ws/terminal/u1-box?ticket=t+k"; got != want {
		t.Errorf("session 0: %q, want %q", got, want)
	}
	if got, want := terminalWSURL("https://api.x", "u1-box", "t", 3), "wss://api.x/ws/terminal/u1-box?ticket=t&persist=1&session=3"; got != want {
		t.Errorf("session 3: %q, want %q", got, want)
	}
}

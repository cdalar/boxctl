package cmd

import (
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

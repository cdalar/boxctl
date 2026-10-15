package client

import (
	"strings"
	"testing"
)

func TestRefusalMessages(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{409, `{"error":"box_limit","limit":2,"boxes":["a","b"],"message":"..."}`,
			"You have 2 active boxes (a, b). Delete one with `boxctl rm <box>` or keep working in an existing one."},
		{503, `{"error":"capacity","waitlisted":true}`, "You're on the list"},
		{503, `{"error":"capacity","waitlisted":false}`, "Try again in a while"},
		{409, "vm already exists\n", "vm already exists (HTTP 409)"},
		{503, `{"error":"something else"}`, `(HTTP 503)`},
	}
	for _, c := range cases {
		got := (&apiError{status: c.status, body: c.body}).Error()
		if !strings.Contains(got, c.want) {
			t.Errorf("%d %s: %q, want it to contain %q", c.status, c.body, got, c.want)
		}
	}
}

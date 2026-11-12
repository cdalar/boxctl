package client

import (
	"context"
	"encoding/json"
	"errors"
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

func TestIsTransient(t *testing.T) {
	for st, want := range map[int]bool{500: true, 502: true, 503: true, 404: false, 409: false, 400: false} {
		if got := IsTransient(&apiError{status: st}); got != want {
			t.Errorf("HTTP %d: %v", st, got)
		}
	}
	if IsTransient(context.Canceled) || IsTransient(nil) || IsTransient(errors.New("x")) {
		t.Error("non-transient errors retried")
	}
	_, err := New("http://127.0.0.1:1", "t").List(context.Background())
	if !IsTransient(err) {
		t.Errorf("connection refused not transient: %v", err)
	}
}

func TestTransferNoticeText(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{"nothing", `{"status":"done"}`, ""},
		{"null readdressed", `{"readdressed":null}`, ""},
		{"warning wins", `{"warning":"careful","cold_boot":true,"readdressed":{"ip":"1.2.3.4"}}`, "careful"},
		{"readdressed only", `{"readdressed":{"ip":"172.16.0.9","previous_ip":"172.16.0.7"}}`, "The box got a new address."},
		{"cold boot only", `{"cold_boot":true}`, "The box's memory state was dropped and it cold-booted."},
		{"both", `{"cold_boot":true,"readdressed":{"ip":"1.2.3.4"}}`, "The box got a new address and its memory state was dropped; it cold-booted."},
		{"blank warning", `{"warning":"  ","cold_boot":true}`, "The box's memory state was dropped and it cold-booted."},
	}
	for _, tc := range cases {
		var st transferStatus
		if err := json.Unmarshal([]byte(tc.json), &st); err != nil {
			t.Fatal(err)
		}
		if got := st.notice().Text(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

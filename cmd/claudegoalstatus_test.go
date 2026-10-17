package cmd

import "testing"

func TestGoalHasPR(t *testing.T) {
	for _, tc := range []struct {
		st   goalStatus
		want bool
	}{
		{goalStatus{Status: "done", PRURL: "https://github.com/o/n/pull/7"}, true},
		{goalStatus{Status: "done"}, false},
		{goalStatus{Status: "failed", PRURL: "https://github.com/o/n/pull/7"}, false},
		{goalStatus{Status: "done", PRURL: "https://evil.example/pull/7"}, false},
		{goalStatus{Status: "running", PRURL: "https://github.com/o/n/pull/7"}, false},
	} {
		if got := tc.st.hasPR(); got != tc.want {
			t.Errorf("%+v: hasPR = %v, want %v", tc.st, got, tc.want)
		}
	}
}

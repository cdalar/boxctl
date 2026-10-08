package cmd

import (
	"reflect"
	"testing"
)

func TestParseVars(t *testing.T) {
	got, err := parseVars([]string{"K3S_VERSION=v1.35.4+k3s1", "URL=https://x/?a=b", "EMPTY="})
	if err != nil {
		t.Fatalf("parseVars: %v", err)
	}
	want := map[string]string{"K3S_VERSION": "v1.35.4+k3s1", "URL": "https://x/?a=b", "EMPTY": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVars = %v, want %v", got, want)
	}

	// A bare NAME is how onctl says "take it from my environment"; there
	// is no such environment to take it from here.
	for _, bad := range []string{"NAME", "=value", ""} {
		if _, err := parseVars([]string{bad}); err == nil {
			t.Errorf("parseVars(%q): no error", bad)
		}
	}
}

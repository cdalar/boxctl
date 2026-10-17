package cmd

import (
	"slices"
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestFilterUnfinished(t *testing.T) {
	goal := map[string]string{"boxctl.run": "goal"}
	vms := []client.VM{
		{Name: "done", Labels: goal, RunStatus: []byte(`{"status":"done"}`)},
		{Name: "running", Labels: goal, RunStatus: []byte(`{"status":"running"}`)},
		{Name: "failed", Labels: goal, RunStatus: []byte(`{"status":"failed"}`)},
		{Name: "silent", Labels: goal},                                 // never reported
		{Name: "garbled", Labels: goal, RunStatus: []byte(`not json`)}, // can't be told
		{Name: "session", Labels: map[string]string{"boxctl.run": "session"}},
		{Name: "plain"},
	}
	for _, c := range []struct {
		names []string
		want  []string
	}{
		{nil, []string{"running", "failed", "silent", "garbled"}},
		{[]string{"done", "session", "plain"}, nil},
		{[]string{"running", "done"}, []string{"running"}},
		{[]string{"missing"}, nil},
	} {
		var got []string
		for _, vm := range filterUnfinished(vms, c.names) {
			got = append(got, vm.Name)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("filterUnfinished(names=%v) = %v, want %v", c.names, got, c.want)
		}
	}
}

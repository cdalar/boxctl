package cmd

import (
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestPRCellOf(t *testing.T) {
	status := `{"status":"done","pr_url":"https://github.com/cdalar/boxctl/pull/96","pr_urls":["https://github.com/cdalar/boxctl/pull/96","https://github.com/cdalar/boxctl-web/pull/7"]}`
	vm := client.VM{Name: "goal-x", RunStatus: []byte(status)}
	row := "goal-x  running  claude-agent  cdalar/boxctl +1  #96 +1"
	c, ok := prCellOf(vm, row)
	if !ok {
		t.Fatal("no cell for a box with a PR")
	}
	want := prCell{col: len(row) - len("#96 +1"), num: "#96", rest: " +1", url: "https://github.com/cdalar/boxctl/pull/96"}
	if c != want {
		t.Errorf("got %+v, want %+v", c, want)
	}

	for name, vm := range map[string]client.VM{
		"no run_status": {Name: "plain"},
		"no PR yet":     {Name: "goal-y", RunStatus: []byte(`{"status":"running"}`)},
		"not GitHub":    {Name: "goal-z", RunStatus: []byte(`{"status":"done","pr_url":"https://example.com/pull/1"}`)},
	} {
		if _, ok := prCellOf(vm, vm.Name+"  running  -"); ok {
			t.Errorf("%s: got a cell", name)
		}
	}
}

func TestParsePRState(t *testing.T) {
	if s, err := parsePRState([]byte(`{"state":"MERGED"}`)); err != nil || s != "MERGED" {
		t.Errorf("got %q, %v", s, err)
	}
	for _, bad := range []string{``, `{}`, `not json`} {
		if _, err := parsePRState([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

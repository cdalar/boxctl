package cmd

import (
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestInProject(t *testing.T) {
	vm := func(name string, labels map[string]string) client.VM { return client.VM{Name: name, Labels: labels} }
	const dir, repo = "/Users/x/boxctl", "cdalar/boxctl"
	for _, c := range []struct {
		vm   client.VM
		want bool
	}{
		// Labeled: the labels decide, whatever the name says.
		{vm("boxctl-fix-a1b2", map[string]string{"boxctl.run": "session", "boxctl.project": dir}), true},
		{vm("boxctl-web-fix-a1b2", map[string]string{"boxctl.run": "session", "boxctl.project": "/Users/x/boxctl-web", "boxctl.repo": "cdalar/boxctl-web"}), false},
		{vm("claude-boxctl-web", map[string]string{"boxctl.run": "box", "boxctl.project": "/Users/x/boxctl-web"}), false}, // claude-boxctl's name plus -web
		{vm("goal-boxctl-x-a1b2", map[string]string{"boxctl.run": "goal", "boxctl.repo": repo}), true},
		{vm("goal-onctl-x-a1b2", map[string]string{"boxctl.run": "goal", "boxctl.repo": "cdalar/onctl"}), false},
		{vm("workspace-x-a1b2", map[string]string{"boxctl.run": "repo", "boxctl.repo": "o/a," + repo}), true},
		// Unlabeled: the project's own box and its --own-box tasks, by name.
		{vm("claude-boxctl", nil), true},
		{vm("kilo-boxctl", nil), true},
		{vm("claude-boxctl-fix", nil), true},
		{vm("boxctl-fix-a1b2", nil), false},
		{vm("spike-rancher", nil), false},
	} {
		if got := inProject(c.vm, dir, repo); got != c.want {
			t.Errorf("inProject(%s, %v) = %v, want %v", c.vm.Name, c.vm.Labels, got, c.want)
		}
	}
}

func TestProjectText(t *testing.T) {
	for _, c := range []struct {
		vm      client.VM
		project string
	}{
		{client.VM{}, "-"},
		{client.VM{Labels: map[string]string{"boxctl.project": "/Users/x/notes"}}, "notes"},
		{client.VM{Labels: map[string]string{"boxctl.repo": "o/a,o/b,o/c"}}, "o/a +2"},
		{client.VM{Labels: map[string]string{"boxctl.repo": "o/a"}, RunStatus: []byte(`{"status":"done","pr_url":"https://github.com/o/a/pull/12"}`)}, "o/a"},
	} {
		if got := projectText(c.vm); got != c.project {
			t.Errorf("projectText(%v) = %q, want %q", c.vm.Labels, got, c.project)
		}
	}
}

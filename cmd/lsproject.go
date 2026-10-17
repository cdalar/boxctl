package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cdalar/boxctl/internal/client"
)

// What ls says about a box's project, and which boxes are a project's
// (ls -p, claude ls), from the box's labels -- sessionLabels, runLabels,
// or boxctl label for a box from before them -- and, for an unlabeled
// box, its name.

// inProject reports whether vm is dir's project's: labeled with dir as
// its project or repo (dir's GitHub repository) among its repositories,
// or, unlabeled, named as an agent's box for dir is (claude-<dir>, and
// claude-<dir>-<task> for an --own-box task). An unlabeled run box isn't
// found: by name a boxctl-web box looks like boxctl's.
func inProject(vm client.VM, dir, repo string) bool {
	if vm.Labels["boxctl.run"] != "" {
		if vm.Labels["boxctl.project"] == dir {
			return true
		}
		return repo != "" && hasRepo(vm, repo)
	}
	slug := projectSlug(dir)
	for _, a := range []*boxAgent{claudeAgent, kiloAgent} {
		main := a.name + "-" + slug
		if vm.Name == main || strings.HasPrefix(vm.Name, main+"-") {
			return true
		}
	}
	return false
}

// hasRepo reports whether repo is one of vm's boxctl.repo.
func hasRepo(vm client.VM, repo string) bool {
	for _, r := range strings.Split(vm.Labels["boxctl.repo"], ",") {
		if r == repo {
			return true
		}
	}
	return false
}

// projectText is ls's PROJECT column: the box's repository (the first,
// "+N" for the rest of a workspace's), or its project directory's name
// for one not on GitHub; "-" for an unlabeled box.
func projectText(vm client.VM) string {
	if r := vm.Labels["boxctl.repo"]; r != "" {
		repos := strings.Split(r, ",")
		if len(repos) > 1 {
			return fmt.Sprintf("%s +%d", repos[0], len(repos)-1)
		}
		return r
	}
	if p := vm.Labels["boxctl.project"]; p != "" {
		return filepath.Base(p)
	}
	return "-"
}

// branchText is ls -o wide's BRANCH column: the run's branch, as it
// reported it or as it was labeled.
func branchText(vm client.VM) string {
	if st, _ := parseGoalStatus(vm.RunStatus); st != nil && st.Branch != "" {
		return st.Branch
	}
	if b := vm.Labels["boxctl.branch"]; b != "" {
		return b
	}
	return "-"
}

var prNumber = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/pull/(\d+)$`)

// prText is ls's PR column: the pull request a goal reported, as #N --
// its repository is in PROJECT -- with "+N" for a workspace's others. The
// box wrote it, so anything but a GitHub pull request URL is left out.
func prText(vm client.VM) string {
	st, _ := parseGoalStatus(vm.RunStatus)
	if st == nil {
		return "-"
	}
	m := prNumber.FindStringSubmatch(st.PRURL)
	if m == nil {
		return "-"
	}
	text := "#" + m[1]
	if n := len(st.PRURLs); n > 1 {
		text += fmt.Sprintf(" +%d", n-1)
	}
	return text
}

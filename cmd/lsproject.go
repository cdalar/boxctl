package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

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

var githubPR = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/pull/\d+$`)

// runSep is between the parts of a box's run line.
const runSep = " · "

// runParts are a box's run line in ls, the line under its row: what
// its run is up to, from its labels and run_status alone -- the run
// (runText), its project (projectText), its branch (branchText) and,
// last, its pull request, each left out when there's nothing to say,
// so none at all for a box nothing labeled. The pull request is the
// full URL -- a terminal makes it a link -- with " +N" for the rest of
// a workspace's; the box wrote it, so anything but a GitHub pull
// request URL is left out. The cell says where on the line the URL
// starts, for fillPRStates to write the state after it.
func runParts(vm client.VM) (parts []string, pr prCell, hasPR bool) {
	for _, p := range []string{runText(vm), projectText(vm), branchText(vm)} {
		if p != "-" {
			parts = append(parts, p)
		}
	}
	st, _ := parseGoalStatus(vm.RunStatus)
	if st == nil || !githubPR.MatchString(st.PRURL) {
		return parts, prCell{}, false
	}
	pr = prCell{url: st.PRURL}
	if n := len(st.PRURLs); n > 1 {
		pr.rest = fmt.Sprintf(" +%d", n-1)
	}
	parts = append(parts, pr.url+pr.rest)
	pr.col = utf8.RuneCountInString(runLine(parts, false)) - len(pr.url+pr.rest)
	return parts, pr, true
}

// runIndent sets a run line in from the box's row above it.
const runIndent = "  "

// runLine is runParts joined as ls prints them, with the separators
// dim and the run's outcome coloured when color.
func runLine(parts []string, color bool) string {
	if len(parts) == 0 {
		return ""
	}
	if !color {
		return runIndent + strings.Join(parts, runSep)
	}
	coloured := slices.Clone(parts)
	if text, status, ok := strings.Cut(parts[0], ": "); ok {
		switch status {
		case "done":
			coloured[0] = text + ": " + paint(ansiGreen, status)
		case "failed":
			coloured[0] = text + ": " + paint(ansiRed, status)
		}
	}
	return runIndent + strings.Join(coloured, dim(runSep))
}

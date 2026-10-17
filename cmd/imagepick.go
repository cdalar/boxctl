package cmd

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
)

// A box made for an agent run, with no --image given, boots the image that
// suits the project: its toolchain is already there. The project's stack
// comes from the files in its directory; which image carries that
// toolchain is the server's to say -- the names come from /api/images and
// are matched, never hard-coded, so a new image needs no CLI change. No
// match, or no way to ask, means the default image.

// stack is a toolchain a project needs, with the file names that show it
// (a "*" suffix matches by extension) and the words an image of it might
// have in its name.
type stack struct {
	name  string
	files []string
	words []string
}

var stacks = []stack{
	{"go", []string{"go.mod", "go.work"}, []string{"go", "golang"}},
	{"rust", []string{"Cargo.toml"}, []string{"rust", "cargo"}},
	{"python", []string{"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"}, []string{"python", "py"}},
	{"node", []string{"package.json"}, []string{"node", "nodejs", "js", "javascript", "typescript"}},
	{"java", []string{"pom.xml", "build.gradle", "build.gradle.kts"}, []string{"java", "jdk"}},
	{"ruby", []string{"Gemfile"}, []string{"ruby"}},
	{"php", []string{"composer.json"}, []string{"php"}},
	{"dotnet", []string{"*.csproj", "*.sln"}, []string{"dotnet", "csharp"}},
}

// detectStack names the first stack dir's top level shows, or nil. The
// order of stacks breaks ties: a Go service with a package.json for its
// frontend is a Go project.
func detectStack(dir string) *stack {
	for i := range stacks {
		for _, f := range stacks[i].files {
			if strings.HasPrefix(f, "*") {
				if m, _ := filepath.Glob(filepath.Join(dir, f)); len(m) > 0 {
					return &stacks[i]
				}
			} else if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
				return &stacks[i]
			}
		}
	}
	return nil
}

// pickImage returns the name of the image in images made for s, or "".
// A name matches when one of s's words is a whole dash-, underscore- or
// dot-separated part of it (so "go" matches "go-dev", not "mongo"). Of
// several, one for the agent (claude-agent-go) wins, then the shortest.
func pickImage(images []client.Image, s *stack, agentName string) string {
	var found []string
	for _, img := range images {
		parts := strings.FieldsFunc(strings.ToLower(img.Name), func(r rune) bool { return r == '-' || r == '_' || r == '.' })
		for _, p := range parts {
			if contains(s.words, p) {
				found = append(found, img.Name)
				break
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.SliceStable(found, func(i, j int) bool {
		ai, aj := strings.Contains(found[i], agentName), strings.Contains(found[j], agentName)
		if ai != aj {
			return ai
		}
		return len(found[i]) < len(found[j])
	})
	return found[0]
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// imageFor is the image for a new box holding dir: --image if given,
// else the one matching dir's stack, else the default. dir is "" when
// there's no local project (a --repo run), which gets the default.
func imageFor(ctx context.Context, c *client.Client, dir string) string {
	if claudeImage != "" {
		return claudeImage
	}
	if dir == "" {
		return claudeDefaultImage
	}
	s := detectStack(dir)
	if s == nil {
		return claudeDefaultImage
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	images, err := c.ListImages(ctx)
	if err != nil {
		return claudeDefaultImage
	}
	if name := pickImage(images, s, agent.name); name != "" {
		return name
	}
	return claudeDefaultImage
}

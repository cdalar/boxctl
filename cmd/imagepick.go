package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	s, _ := detectStackFile(dir)
	return s
}

// detectStackFile is detectStack, also naming the file that showed it.
func detectStackFile(dir string) (*stack, string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, ""
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return stackOfFiles(names)
}

// stackOfFiles is detectStack over a list of top-level file names, and
// the one that matched.
func stackOfFiles(names []string) (*stack, string) {
	for i := range stacks {
		for _, f := range stacks[i].files {
			for _, n := range names {
				if f == n || (strings.HasPrefix(f, "*") && strings.HasSuffix(n, f[1:])) {
					return &stacks[i], n
				}
			}
		}
	}
	return nil, ""
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
// there's no local project. why says what decided it ("" for the default
// with nothing to go on).
func imageFor(ctx context.Context, c *client.Client, dir string) (image, why string) {
	if claudeImage != "" {
		return claudeImage, "--image"
	}
	if dir == "" {
		return claudeDefaultImage, ""
	}
	s, file := detectStackFile(dir)
	return imageForStack(ctx, c, s, file, filepath.Base(dir))
}

// imageForStack matches s against the server's images; src is what the
// stack was read from, for the message.
func imageForStack(ctx context.Context, c *client.Client, s *stack, file, src string) (string, string) {
	if s == nil {
		return claudeDefaultImage, "no known stack in " + src
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	images, err := c.ListImages(ctx)
	if err != nil {
		return claudeDefaultImage, "couldn't list images"
	}
	if name := pickImage(images, s, agent.name); name != "" {
		return name, file + " in " + src
	}
	return claudeDefaultImage, "no " + s.name + " image (" + file + " in " + src + ")"
}

// githubAPI is the GitHub REST API's base URL; a variable for tests.
var githubAPI = "https://api.github.com"

// repoRootFiles lists the names at the root of owner/name, with token if
// there is one: one call, five seconds.
func repoRootFiles(ctx context.Context, repo, token string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/repos/"+repo+"/contents/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&items); err != nil {
		return nil, err
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	return names, nil
}

// runImage is the image for a --repo/--goal run: --image, else the stack
// of the local project (dir, when the run has no --repo), else of the
// first repository's root files from GitHub, else the default. Any
// error on the way means the default.
func runImage(ctx context.Context, c *client.Client, dir string, repos []string, token string) (image, why string) {
	if claudeImage != "" || dir != "" {
		return imageFor(ctx, c, dir)
	}
	if len(repos) == 0 {
		return claudeDefaultImage, ""
	}
	names, err := repoRootFiles(ctx, repos[0], token)
	if err != nil {
		return claudeDefaultImage, "couldn't read " + repos[0] + " from GitHub: " + err.Error()
	}
	s, file := stackOfFiles(names)
	image, why = imageForStack(ctx, c, s, file, repos[0])
	if len(repos) > 1 {
		why += "; first of " + fmt.Sprint(len(repos)) + " repositories"
	}
	return image, why
}

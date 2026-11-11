package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdalar/boxctl/internal/client"
)

func TestDetectStack(t *testing.T) {
	dir := t.TempDir()
	if detectStack(dir) != nil {
		t.Fatal("empty dir has a stack")
	}
	for _, f := range []string{"package.json", "go.mod"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if s := detectStack(dir); s == nil || s.name != "go" {
		t.Fatalf("got %v, want go first", s)
	}
	d2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(d2, "App.csproj"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if s := detectStack(d2); s == nil || s.name != "dotnet" {
		t.Fatalf("got %v, want dotnet", s)
	}
}

func TestPickImage(t *testing.T) {
	imgs := []client.Image{{Name: "debian-slim"}, {Name: "mongo-tools"}, {Name: "golang-full"}, {Name: "claude-agent-go"}, {Name: "go"}}
	goStack := &stacks[0]
	if got := pickImage(imgs, goStack, "claude"); got != "claude-agent-go" {
		t.Errorf("got %q", got)
	}
	if got := pickImage(imgs[:2], goStack, "claude"); got != "" {
		t.Errorf("mongo matched go: %q", got)
	}
}

// fakeImageAPIs serves boxctl's /api/images and GitHub's repo contents.
// files maps "owner/name" to its root file names; a repo missing from it
// is a 404, and images == nil makes /api/images fail.
func fakeImageAPIs(t *testing.T, images []string, files map[string][]string) (*client.Client, *int) {
	t.Helper()
	calls := new(int)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/contents/")
		names, ok := files[repo]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		var items []map[string]string
		for _, n := range names {
			items = append(items, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if images == nil {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var out []map[string]string
		for _, n := range images {
			out = append(out, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	oldGH, oldAgent, oldImage := githubAPI, agent, claudeImage
	githubAPI, agent, claudeImage = gh.URL, claudeAgent, ""
	t.Cleanup(func() {
		gh.Close()
		api.Close()
		githubAPI, agent, claudeImage = oldGH, oldAgent, oldImage
	})
	return client.New(api.URL, "t"), calls
}

func TestRunImage(t *testing.T) {
	imgs := []string{"debian-slim", "claude-agent", "claude-agent-go"}
	files := map[string][]string{
		"o/goapp":  {"README.md", "go.mod", "main.go"},
		"o/plain":  {"README.md", "notes.txt"},
		"o/rusty":  {"Cargo.toml"},
		"o/second": {"package.json"},
	}
	goDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bareDir := t.TempDir()
	ctx := context.Background()

	cases := []struct {
		name      string
		images    []string
		dir       string
		repos     []string
		token     string
		image     string
		whyHas    string
		image_ovr string
		wantCalls int
	}{
		{"local go project", imgs, goDir, []string{"o/goapp"}, "tok", "claude-agent-go", "go.mod in " + filepath.Base(goDir), "", 0},
		{"repo go", imgs, "", []string{"o/goapp"}, "tok", "claude-agent-go", "go.mod in o/goapp", "", 1},
		{"repo no stack", imgs, "", []string{"o/plain"}, "tok", "claude-agent", "no known stack in o/plain", "", 1},
		{"repo stack without image", imgs, "", []string{"o/rusty"}, "tok", "claude-agent", "no rust image", "", 1},
		{"github error", imgs, "", []string{"o/missing"}, "tok", "claude-agent", "couldn't read o/missing", "", 1},
		{"github rejects token", imgs, "", []string{"o/goapp"}, "bad", "claude-agent", "401", "", 1},
		{"images api error", nil, "", []string{"o/goapp"}, "tok", "claude-agent", "couldn't list images", "", 1},
		{"first of several", imgs, "", []string{"o/goapp", "o/second"}, "tok", "claude-agent-go", "first of 2 repositories", "", 1},
		{"--image wins (repo)", imgs, "", []string{"o/goapp"}, "tok", "mine", "--image", "mine", 0},
		{"--image wins (local)", imgs, goDir, nil, "tok", "mine", "--image", "mine", 0},
		{"local dir, no stack", imgs, bareDir, nil, "tok", "claude-agent", "no known stack", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, calls := fakeImageAPIs(t, c.images, files)
			claudeImage = c.image_ovr
			got, why := runImage(ctx, cl, c.dir, c.repos, c.token)
			if got != c.image || !strings.Contains(why, c.whyHas) {
				t.Errorf("got %q (%s), want %q (%s)", got, why, c.image, c.whyHas)
			}
			if *calls != c.wantCalls {
				t.Errorf("%d GitHub calls, want %d", *calls, c.wantCalls)
			}
		})
	}
}

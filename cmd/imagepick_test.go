package cmd

import (
	"os"
	"path/filepath"
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

package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func importsFor(t *testing.T, claudeMD string, others map[string]string) (files, warnings []string, home string) {
	t.Helper()
	home = t.TempDir()
	src := filepath.Join(home, ".claude")
	write(t, filepath.Join(src, "CLAUDE.md"), claudeMD)
	for name, body := range others {
		write(t, filepath.Join(src, name), body)
	}
	files, warnings = importedConfigFiles(src, home)
	return
}

func TestImportsAgentsMd(t *testing.T) {
	files, warns, home := importsFor(t, "@AGENTS.md\n", map[string]string{"AGENTS.md": "rules"})
	if want := []string{"AGENTS.md"}; !reflect.DeepEqual(files, want) || len(warns) != 0 {
		t.Errorf("files %v warnings %v", files, warns)
	}
	all, size, rules, _, err := configFilesWithImports(filepath.Join(home, ".claude"), home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules, []string{"CLAUDE.md", "AGENTS.md"}) || !reflect.DeepEqual(all, []string{"CLAUDE.md", "AGENTS.md"}) || size != int64(len("@AGENTS.md\n")+len("rules")) {
		t.Errorf("all %v rules %v size %d", all, rules, size)
	}
}

func TestImportsNestedAndHome(t *testing.T) {
	files, warns, _ := importsFor(t, "see @docs/a.md and @~/.claude/b.md\n", map[string]string{
		"docs/a.md": "@sub/c.md", "docs/sub/c.md": "leaf", "b.md": "b",
	})
	if want := []string{"docs/a.md", "docs/sub/c.md", "b.md"}; !reflect.DeepEqual(files, want) || len(warns) != 0 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsMissingWarnsButMentionAndEmailDoNot(t *testing.T) {
	files, warns, _ := importsFor(t, "@gone.md\nping @alice, mail bob@example.com\n", nil)
	if len(files) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "@gone.md") {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsOutsideClaudeDir(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "notes.md"), "outside")
	src := filepath.Join(home, ".claude")
	write(t, filepath.Join(src, "CLAUDE.md"), "@../notes.md\n@~/notes.md\n")
	files, warns := importedConfigFiles(src, home)
	if len(files) != 0 || len(warns) != 2 || !strings.Contains(warns[0], "outside ~/.claude") {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsSymlinkOutside(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "secret.md"), "x")
	src := filepath.Join(home, ".claude")
	write(t, filepath.Join(src, "CLAUDE.md"), "@link.md\n")
	if err := os.Symlink(filepath.Join(home, "secret.md"), filepath.Join(src, "link.md")); err != nil {
		t.Skip(err)
	}
	files, warns := importedConfigFiles(src, home)
	if len(files) != 0 || len(warns) != 1 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsLoop(t *testing.T) {
	files, warns, _ := importsFor(t, "@a.md\n", map[string]string{"a.md": "@b.md", "b.md": "@a.md @CLAUDE.md"})
	if want := []string{"a.md", "b.md"}; !reflect.DeepEqual(files, want) || len(warns) != 0 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsDepthLimit(t *testing.T) {
	others := map[string]string{}
	for i := 1; i <= 8; i++ {
		others[string(rune('a'+i-1))+".md"] = "@" + string(rune('a'+i)) + ".md"
	}
	files, warns, _ := importsFor(t, "@a.md", others)
	if len(files) != maxImportDepth || len(warns) != 1 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsInCode(t *testing.T) {
	md := "use `@inline.md` here\n```\n@fenced.md\n```\n~~~\n@tilde.md\n~~~\n@real.md\n"
	files, warns, _ := importsFor(t, md, map[string]string{
		"inline.md": "x", "fenced.md": "x", "tilde.md": "x", "real.md": "x"})
	if want := []string{"real.md"}; !reflect.DeepEqual(files, want) || len(warns) != 0 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

func TestImportsNeverIntoExcluded(t *testing.T) {
	md := "@projects/p.md\n@.credentials.json\n@settings.json\n@skills/s/node_modules/x.md\n"
	files, warns, _ := importsFor(t, md, map[string]string{
		"projects/p.md": "x", ".credentials.json": "{}", "settings.json": "{}", "skills/s/node_modules/x.md": "x"})
	if len(files) != 0 || len(warns) != 4 {
		t.Errorf("files %v warnings %v", files, warns)
	}
}

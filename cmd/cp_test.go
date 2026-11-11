package cmd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCp(t *testing.T) {
	ok := []struct {
		src, dst string
		r        bool
		want     cpPlan
	}{
		{"box:/root/a", ".", false, cpPlan{box: "box", remote: "/root/a", local: "."}},
		{"./out", "box:/srv/", true, cpPlan{box: "box", remote: "/srv/", local: "./out", upload: true, recursive: true}},
		{"./a:b", "box:x", false, cpPlan{box: "box", remote: "x", local: "./a:b", upload: true}},
		{"my-box:dir", "/tmp/x", true, cpPlan{box: "my-box", remote: "dir", local: "/tmp/x", recursive: true}},
	}
	for _, c := range ok {
		got, err := parseCp(c.src, c.dst, c.r)
		if err != nil || *got != c.want {
			t.Errorf("%s %s: got %+v, %v; want %+v", c.src, c.dst, got, err, c.want)
		}
	}
	if _, err := parseCp("a:/x", "b:/y", false); err == nil || !strings.Contains(err.Error(), "both sides") {
		t.Errorf("both remote: %v", err)
	}
	if _, err := parseCp("x", "y", false); err == nil {
		t.Error("neither remote should fail")
	}
}

// fakeCopyTools puts a fake ssh, scp and (if withRsync) rsync on PATH,
// ahead of nothing else: the real ones are hidden. "root@claude-x:/p" is
// $FAKE_ROOT/p, and scp/rsync just cp -r between their last two arguments.
// The fake ssh answers `test -d/-e` and `command -v rsync`.
func fakeCopyTools(t *testing.T, root string, withRsync bool) {
	t.Helper()
	dir := t.TempDir()
	cp := `#!/bin/sh
echo "$0" >>"$FAKE_ROOT/.used"
for a in "$@"; do
  case "$a" in
    root@claude-x:*) a="${a#root@claude-x:}"; a="${a#\'}"; a="${a%\'}"; a="$FAKE_ROOT$a" ;;
  esac
  prev="$cur"; cur="$a"
done
cp -r "$prev" "$cur"
`
	rs := "exit 1"
	if withRsync {
		rs = "exit 0"
	}
	ssh := `#!/bin/sh
for a in "$@"; do last="$a"; done
case "$last" in
  "test -d "*) p="${last#test -d }"; p="${p#\'}"; p="${p%\'}"; [ -d "$FAKE_ROOT$p" ] ;;
  "test -e "*) p="${last#test -e }"; p="${p#\'}"; p="${p%\'}"; [ -e "$FAKE_ROOT$p" ] ;;
  "command -v rsync"*) ` + rs + ` ;;
esac
`
	tools := map[string]string{"scp": cp, "ssh": ssh}
	if withRsync {
		tools["rsync"] = cp
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"sh", "cp", "echo", "test"} {
		if p, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(p, filepath.Join(dir, tool))
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_ROOT", root)
}

func sumTree(t *testing.T, dir string) map[string][sha256.Size]byte {
	t.Helper()
	out := map[string][sha256.Size]byte{}
	files, err := listFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f.path))
		if err != nil {
			t.Fatal(err)
		}
		out[f.path] = sha256.Sum256(b)
	}
	return out
}

func TestCopyRoundTrip(t *testing.T) {
	for _, withRsync := range []bool{false, true} {
		root := t.TempDir()
		fakeCopyTools(t, root, withRsync)
		src := filepath.Join(t.TempDir(), "shots")
		if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		big := make([]byte, 5<<20+123)
		_, _ = rand.Read(big)
		for name, data := range map[string][]byte{"big.bin": big, "sub/small.txt": []byte("hi\n")} {
			if err := os.WriteFile(filepath.Join(src, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		ctx := context.Background()
		up, err := parseCp(src, "claude-x:/shots", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := up.run(ctx, testBox()); err != nil {
			t.Fatalf("rsync=%v upload: %v", withRsync, err)
		}
		back := filepath.Join(t.TempDir(), "back")
		down, _ := parseCp("claude-x:/shots", back, true)
		if err := down.run(ctx, testBox()); err != nil {
			t.Fatalf("rsync=%v download: %v", withRsync, err)
		}
		want, got := sumTree(t, src), sumTree(t, back)
		if len(want) != 2 || len(got) != len(want) {
			t.Fatalf("rsync=%v: files %v, want %v", withRsync, got, want)
		}
		for f, sum := range want {
			if got[f] != sum {
				t.Errorf("rsync=%v: %s differs after the round trip", withRsync, f)
			}
		}
		used, _ := os.ReadFile(filepath.Join(root, ".used"))
		if strings.Contains(string(used), "rsync") != withRsync {
			t.Errorf("rsync available=%v, tools used: %s", withRsync, used)
		}
	}
}

func TestCopyDirectoryNeedsR(t *testing.T) {
	root := t.TempDir()
	fakeCopyTools(t, root, false)
	_ = os.MkdirAll(filepath.Join(root, "d"), 0o755)
	p, _ := parseCp("claude-x:/d", t.TempDir(), false)
	if err := p.run(context.Background(), testBox()); err == nil || !strings.Contains(err.Error(), "-r") {
		t.Errorf("remote dir without -r: %v", err)
	}
	p, _ = parseCp(t.TempDir(), "claude-x:/d", false)
	if err := p.run(context.Background(), testBox()); err == nil || !strings.Contains(err.Error(), "-r") {
		t.Errorf("local dir without -r: %v", err)
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(5<<20 + 1<<19); got != "5.5 MiB" || humanBytes(12) != "12 B" {
		t.Errorf("got %q", got)
	}
}

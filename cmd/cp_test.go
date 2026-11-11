package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// fakeBoxSSH puts a fake ssh on PATH for the checks that don't need a real
// copy. "test -d P" looks in $FAKE_ROOT/P, and the evidence tar runs the
// real tar on $FAKE_ROOT/root/evidence.
func fakeBoxSSH(t *testing.T, root string) {
	t.Helper()
	dir := t.TempDir()
	ssh := `#!/bin/sh
for a in "$@"; do last="$a"; done
case "$last" in
  "test -d "*) p="${last#test -d }"; p="${p#\'}"; p="${p%\'}"; [ -d "$FAKE_ROOT$p" ] ;;
  "tar -C /root/evidence"*) exec tar -C "$FAKE_ROOT/root/evidence" -cf - . ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "tar"} {
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

func TestCopyDirectoryNeedsR(t *testing.T) {
	root := t.TempDir()
	fakeBoxSSH(t, root)
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

func TestScpArgs(t *testing.T) {
	b := &boxSSH{name: "x", args: []string{"-F", "/dev/null", "-o", "ProxyCommand=a b", "root@claude-x"}}
	pre := []string{"-o", "BatchMode=yes", "-F", "/dev/null", "-o", "ProxyCommand=a b"}
	cases := []struct {
		p       cpPlan
		quiet   bool
		windows bool
		tail    []string
	}{
		{cpPlan{remote: "/r/a b", local: "-v"}, true, false, []string{"-q", "--", "root@claude-x:/r/a b", "./-v"}},
		{cpPlan{remote: "/r", local: "-oProxyCommand=x", upload: true, recursive: true}, false, false, []string{"-r", "--", "./-oProxyCommand=x", "root@claude-x:/r"}},
		{cpPlan{remote: "/r", local: "foo:", upload: true}, false, false, []string{"--", "./foo:", "root@claude-x:/r"}},
		{cpPlan{remote: "/r", local: "a:b"}, false, false, []string{"--", "root@claude-x:/r", "./a:b"}},
		{cpPlan{remote: "/r", local: "./a:b"}, false, false, []string{"--", "root@claude-x:/r", "./a:b"}},
		{cpPlan{remote: "/r", local: "/tmp/a:b"}, false, false, []string{"--", "root@claude-x:/r", "/tmp/a:b"}},
		{cpPlan{remote: "/r", local: `C:\out`}, false, true, []string{"--", "root@claude-x:/r", `C:\out`}},
	}
	for _, c := range cases {
		want := append(append([]string{}, pre...), c.tail...)
		if got := b.scpArgs(&c.p, c.quiet, c.windows); !reflect.DeepEqual(got, want) {
			t.Errorf("%+v:\n got %q\nwant %q", c.p, got, want)
		}
	}
}

func TestSplitBoxPath(t *testing.T) {
	cases := []struct {
		in      string
		windows bool
		box     string // "" means local
	}{
		{`C:\out`, true, ""}, {"C:/out", true, ""}, {"C:", true, ""}, {"c:rel", true, "c"},
		{`C:\out`, false, "C"}, {"C:/out", false, "C"}, // on Unix a box can be named C
		{"foo:", false, ""}, {"./a:b", false, ""}, {"a:b", false, "a"}, {"box:/x", false, "box"}, {":x", false, ""}, {"plain", false, ""},
	}
	for _, c := range cases {
		box, _, remote := splitBoxPath(c.in, c.windows)
		if (c.box != "") != remote || box != c.box {
			t.Errorf("%q windows=%v: box %q remote=%v, want %q", c.in, c.windows, box, remote, c.box)
		}
	}
}

func TestExtractTarSkipsLinksAndRefusesEscapes(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		_ = tw.WriteHeader(&h)
		_, _ = tw.Write([]byte(body))
	}
	add(tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(tar.Header{Name: "./a.png", Typeflag: tar.TypeReg}, "png")
	add(tar.Header{Name: "./sub/b.txt", Typeflag: tar.TypeReg}, "bb")
	add(tar.Header{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "/"}, "")
	_ = tw.Close()
	root := t.TempDir()
	files, skipped, err := extractTar(bytes.NewReader(buf.Bytes()), root)
	if err != nil || len(files) != 2 || len(skipped) != 1 || files[0].path != "a.png" || files[1].path != "sub/b.txt" {
		t.Fatalf("files %v skipped %v err %v", files, skipped, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); err == nil {
		t.Error("symlink was created")
	}
	for _, name := range []string{"../evil", "/abs", "./a/../../evil", ".boxctl-evidence"} {
		buf.Reset()
		tw = tar.NewWriter(&buf)
		add(tar.Header{Name: name, Typeflag: tar.TypeReg}, "x")
		_ = tw.Close()
		if _, _, err := extractTar(&buf, t.TempDir()); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
}

func TestFetchEvidence(t *testing.T) {
	root := t.TempDir()
	fakeBoxSSH(t, root)
	ev := filepath.Join(root, "root", "evidence")
	_ = os.MkdirAll(filepath.Join(ev, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(ev, "a.png"), []byte("png"), 0o644)
	_ = os.WriteFile(filepath.Join(ev, "sub", "b.txt"), []byte("bb"), 0o644)
	_ = os.Symlink("/etc", filepath.Join(ev, "etc"))
	_ = os.Symlink("..", filepath.Join(ev, "loop"))

	work := t.TempDir()
	t.Chdir(work)
	dest := filepath.Join("evidence", "claude-x")
	_ = os.MkdirAll(dest, 0o755)
	_ = os.WriteFile(filepath.Join(dest, "stale.png"), []byte("old"), 0o644)
	if err := fetchEvidence(context.Background(), testBox(), "claude-x", dest, true); err != nil {
		t.Fatal(err)
	}
	files, _ := listFiles(dest)
	if len(files) != 2 || files[0].path != "a.png" || files[1].path != "sub/b.txt" {
		t.Errorf("after fetch: %v (the stale file should be gone, links skipped)", files)
	}
	if ents, _ := os.ReadDir("evidence"); len(ents) != 1 {
		t.Errorf("leftovers beside the destination: %v", ents)
	}
	// An empty evidence dir leaves the earlier fetch alone.
	_ = os.RemoveAll(ev)
	_ = os.MkdirAll(ev, 0o755)
	if err := fetchEvidence(context.Background(), testBox(), "claude-x", dest, true); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty evidence: %v", err)
	}
	if files, _ := listFiles(dest); len(files) != 2 {
		t.Errorf("an empty fetch destroyed the earlier one: %v", files)
	}
	// A directory of the user's own is not replaced.
	own := filepath.Join(work, "mine")
	_ = os.MkdirAll(own, 0o755)
	_ = os.WriteFile(filepath.Join(own, "keep.txt"), []byte("x"), 0o644)
	if err := fetchEvidence(context.Background(), testBox(), "claude-x", own, false); err == nil {
		t.Error("replaced a directory that isn't an earlier fetch")
	}
	// Nor the one holding the current directory.
	if err := fetchEvidence(context.Background(), testBox(), "claude-x", ".", false); err == nil {
		t.Error("accepted the current directory")
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(5<<20 + 1<<19); got != "5.5 MiB" || humanBytes(12) != "12 B" {
		t.Errorf("got %q", got)
	}
}

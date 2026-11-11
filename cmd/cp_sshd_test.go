package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// throwawaySSHD starts a real sshd on a free localhost port, accepting one
// fresh key, and returns a boxSSH that reaches it. The test is skipped when
// sshd, ssh-keygen or scp isn't installed or sshd won't start. It "is" the
// box: remote paths are paths on this machine.
func throwawaySSHD(t *testing.T) *boxSSH {
	t.Helper()
	var sshd string
	for _, p := range []string{"sshd", "/usr/sbin/sshd", "/usr/local/sbin/sshd"} {
		if f, err := exec.LookPath(p); err == nil {
			sshd = f
			break
		}
	}
	for _, tool := range []string{"ssh", "scp", "ssh-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	if sshd == "" {
		t.Skip("sshd is not installed")
	}
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	keygen := func(name string) string {
		k := filepath.Join(dir, name)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", k).CombinedOutput(); err != nil {
			t.Skipf("ssh-keygen: %v: %s", err, out)
		}
		return k
	}
	host, client := keygen("host"), keygen("client")
	pub, _ := os.ReadFile(client + ".pub")
	auth := filepath.Join(dir, "authorized_keys")
	_ = os.WriteFile(auth, pub, 0o600)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	conf := filepath.Join(dir, "sshd_config")
	_ = os.WriteFile(conf, []byte(fmt.Sprintf(`ListenAddress 127.0.0.1
Port %d
HostKey %s
AuthorizedKeysFile %s
PidFile %s
StrictModes no
UsePAM no
PasswordAuthentication no
PermitRootLogin yes
Subsystem sftp internal-sftp
`, port, host, auth, filepath.Join(dir, "pid"))), 0o600)
	_ = os.MkdirAll("/run/sshd", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", conf)
	var log bytes.Buffer
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Skipf("sshd: %v", err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			_ = c.Close()
			b := &boxSSH{name: "localhost", args: []string{
				"-F", "/dev/null", "-i", client, "-o", "IdentitiesOnly=yes",
				"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
				"-o", fmt.Sprintf("Port=%d", port), u.Username + "@127.0.0.1",
			}}
			if b.run(context.Background(), "true", nil) != nil {
				t.Skipf("can't ssh into the throwaway sshd: %s", log.String())
			}
			return b
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Skipf("sshd did not start: %s", log.String())
	return nil
}

// TestCopyThroughRealSSHD does real scp round trips: a multi-megabyte
// binary and a directory tree, up and down, plus a local file named -v.
func TestCopyThroughRealSSHD(t *testing.T) {
	box := throwawaySSHD(t)
	ctx := context.Background()
	t.Chdir(t.TempDir())

	big := make([]byte, 6<<20+17)
	_, _ = rand.Read(big)
	if err := os.WriteFile("-v", big, 0o644); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	cp := func(src, dst string, r bool) {
		t.Helper()
		p, err := parseCp(src, dst, r)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.run(ctx, box); err != nil {
			t.Fatalf("cp %s %s: %v", src, dst, err)
		}
	}
	// The local file is named "-v"; the remote, "have:colon", would be a host.
	cp("./-v", "localhost:"+remote+"/big.bin", false)
	cp("localhost:"+remote+"/big.bin", "./got:colon", false)
	got, err := os.ReadFile("got:colon")
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("binary differs after the round trip: %v", err)
	}
	if err := os.Remove("-v"); err != nil {
		t.Fatal(err)
	}
	cp("localhost:"+remote+"/big.bin", "-v", false) // a destination named -v
	if got, err := os.ReadFile("-v"); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("download to a dash-named file: %v", err)
	}

	tree := filepath.Join(t.TempDir(), "tree")
	_ = os.MkdirAll(filepath.Join(tree, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(tree, "a.bin"), big[:1<<20], 0o644)
	_ = os.WriteFile(filepath.Join(tree, "sub", "b with space.txt"), []byte("hi\n"), 0o644)
	cp(tree, "localhost:"+remote+"/tree", true)
	back := filepath.Join(t.TempDir(), "back")
	cp("localhost:"+remote+"/tree", back, true)
	want, have := sumTree(t, tree), sumTree(t, back)
	if len(want) != 2 || len(have) != 2 {
		t.Fatalf("files: %v, want %v", have, want)
	}
	for f, sum := range want {
		if have[f] != sum {
			t.Errorf("%s differs after the round trip", f)
		}
	}
}

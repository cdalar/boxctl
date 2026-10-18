package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// stdinFile returns a plain file holding content, opened for reading:
// not a terminal, so readToken takes its first line from it.
func stdinFile(t *testing.T, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestReadToken(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		stdin   string
		want    string
		wantErr bool
	}{
		{name: "env wins over stdin", env: "env-token", stdin: "stdin-token\n", want: "env-token"},
		{name: "env is trimmed", env: "  env-token\n", want: "env-token"},
		{name: "blank env falls back to stdin", env: "  ", stdin: "stdin-token\n", want: "stdin-token"},
		{name: "stdin first line only", stdin: "stdin-token\nsecond\n", want: "stdin-token"},
		{name: "stdin without newline", stdin: "stdin-token", want: "stdin-token"},
		{name: "nothing anywhere", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readToken(tt.env, stdinFile(t, tt.stdin))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("readToken() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readToken() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("readToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

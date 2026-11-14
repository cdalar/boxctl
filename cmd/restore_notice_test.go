package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/boxctl-io/boxctl-cli/internal/client"
)

func TestPrintTransferNotice(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	run := func(n client.TransferNotice) string {
		old := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w
		printTransferNotice(n)
		_ = w.Close()
		os.Stderr = old
		b, _ := io.ReadAll(r)
		return string(b)
	}
	if got := run(client.TransferNotice{}); got != "" {
		t.Errorf("empty notice printed %q", got)
	}
	if got := run(client.TransferNotice{Warning: "heads up"}); !strings.Contains(got, "heads up") {
		t.Errorf("warning not printed: %q", got)
	}
	if got := run(client.TransferNotice{Readdressed: true, ColdBoot: true}); !strings.Contains(got, "new address") || !strings.Contains(got, "cold-booted") {
		t.Errorf("fallback not printed: %q", got)
	}
}

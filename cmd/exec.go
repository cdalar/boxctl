package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var (
	execImage   string
	execTimeout time.Duration
)

// execReadyTimeout bounds how long runExec waits for the ephemeral box
// it just created to become reachable (see Client.WaitReady) -- separate
// from execTimeout, which bounds the command itself once the box is
// ready.
const execReadyTimeout = 60 * time.Second

var execCmd = &cobra.Command{
	Use:   "exec <command>",
	Short: "Run a command in a fresh, disposable box and print the result as JSON",
	Long: `Creates a new box, waits for it to become reachable, runs command in
it over SSH (no pty -- exit code, stdout, and stderr come back
separately), then destroys the box regardless of how the command went.

Prints exactly one JSON object to stdout:

  {"exit_code":0,"stdout":"...","stderr":"...","error":""}

"error" is only set for a transport-level failure (couldn't reach the
box at all, or the command timed out) -- a normal nonzero exit from the
command itself is NOT an error, it's exit_code alone.

Meant for driving from a script or an AI agent: progress goes to
stderr, stdout is always exactly one JSON object, and the process exits
with the command's own exit code (or 1 on a transport-level failure) --
so both "parse stdout as JSON" and "check $?" work.

command is a single shell string, e.g.:

  boxctl exec "ls -la | grep foo"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		code, err := runExec(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		os.Exit(code) // deferred cleanup inside runExec has already run
		return nil    // unreachable
	},
}

func init() {
	execCmd.Flags().StringVarP(&execImage, "image", "i", "", "boot image to use (defaults to boxctl-vms's own default)")
	execCmd.Flags().DurationVarP(&execTimeout, "timeout", "T", 30*time.Second, "how long to let the command run before it's killed")
	rootCmd.AddCommand(execCmd)
}

func runExec(ctx context.Context, command string) (exitCode int, err error) {
	c := newClient()
	name := "agent-exec-" + randomHex(8)

	fmt.Fprintf(os.Stderr, "Creating %s...\n", name)
	if _, err := c.Create(ctx, name, "", execImage); err != nil {
		return 0, fmt.Errorf("creating %s: %w", name, err)
	}
	defer func() {
		fmt.Fprintf(os.Stderr, "Destroying %s...\n", name)
		if derr := c.Destroy(context.Background(), name); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to destroy %s: %v\n", name, derr)
		}
	}()

	if _, err := c.WaitReady(ctx, name, execReadyTimeout); err != nil {
		return 0, fmt.Errorf("waiting for %s to become ready: %w", name, err)
	}

	result, err := c.Exec(ctx, name, command, execTimeout)
	if err != nil {
		return 0, fmt.Errorf("running command on %s: %w", name, err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return 0, err
	}

	if result.Error != "" {
		return 1, nil
	}
	return result.ExitCode, nil
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/cdalar/boxctl/internal/client"
	"github.com/spf13/cobra"
)

// defaultImage is what create and exec boot when --image isn't given.
// boxctl-vms has no default of its own (a create without an image is a
// 400), so the choice lives here -- keep it in step with boxctl-web's
// DEFAULT_IMAGE (lib/images.ts).
const defaultImage = "debian-slim"

var (
	createApplyFile string
	createImage     string
	createSize      string
)

var createCmd = &cobra.Command{
	Use:     "create <name>",
	Aliases: []string{"up"},
	Short:   "Create a new box",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		fmt.Printf("Creating %s...\n", name)

		c := newClient()
		if createApplyFile == "" {
			vm, err := c.Create(cmd.Context(), name, "", createImage, createSize)
			if err != nil {
				return err
			}
			fmt.Printf("Created %s (%s, %s)\n", vm.Name, vm.State, sizeLabel(*vm))
			return nil
		}

		// The server answers only once the apply script has finished
		// (minutes, for something like Rancher), so tail its log off the
		// box meanwhile instead of sitting silent the whole time.
		tailCtx, stopTail := context.WithCancel(cmd.Context())
		tailDone := make(chan struct{})
		t := &applyLogTail{c: c, box: name, log: "output-" + path.Base(createApplyFile) + ".log"}
		go func() {
			defer close(tailDone)
			t.follow(tailCtx)
		}()

		vm, err := c.Create(cmd.Context(), name, createApplyFile, createImage, createSize)
		stopTail()
		<-tailDone
		// Whatever the script wrote since the last poll -- best effort,
		// the box may not even exist if the create failed.
		drainCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		t.poll(drainCtx)
		cancel()
		if err != nil {
			return err
		}
		fmt.Printf("Created %s (%s, %s)\n", vm.Name, vm.State, sizeLabel(*vm))
		return nil
	},
}

// applyLogPollInterval paces applyLogTail.follow.
const applyLogPollInterval = 2 * time.Second

// applyLogTail relays an apply script's output while create is still
// waiting on it. onctl's CopyAndRunRemoteFile runs the script as
// `./script > ~/.onctl/applyNN/output-<script>.log 2>&1` on the box, so
// this polls that file over Client.Exec (the box runs it as root, same
// user onctl applies as) and prints whatever was appended since the
// last poll. Exec fails until the box is listed, running and ready --
// those errors just mean "not yet" and are ignored.
type applyLogTail struct {
	c   *client.Client
	box string
	log string // file name inside the applyNN dir
	off int64  // bytes of the log already printed
}

func (t *applyLogTail) follow(ctx context.Context) {
	for {
		t.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(applyLogPollInterval):
		}
	}
}

// poll prints the log's bytes past t.off. The file's size is read first
// and reported on stderr, so the new offset comes from the box itself
// rather than from len(stdout) -- which a JSON round trip of non-UTF-8
// output would skew.
func (t *applyLogTail) poll(ctx context.Context) {
	command := fmt.Sprintf(`f=$(ls -t ~/.onctl/apply*/'%s' 2>/dev/null | head -n1); [ -n "$f" ] || exit 0; s=$(stat -c %%s "$f"); tail -c +%d "$f" | head -c $((s-%d)); echo "$s" >&2`,
		t.log, t.off+1, t.off)
	res, err := t.c.Exec(ctx, t.box, command, 10*time.Second)
	if err != nil || res.Error != "" || res.ExitCode != 0 {
		return
	}
	size, err := strconv.ParseInt(strings.TrimSpace(res.Stderr), 10, 64)
	if err != nil || size < t.off {
		return
	}
	if t.off == 0 && size > 0 {
		fmt.Fprintf(os.Stderr, "--- %s output ---\n", createApplyFile)
	}
	fmt.Print(res.Stdout)
	t.off = size
}

func init() {
	createCmd.Flags().StringVarP(&createApplyFile, "apply-file", "a", "", "onctl-templates script to run on the new box, like onctl up -a (e.g. k3s/k3s-server.sh)")
	// The flag's original name, kept so existing commands still work.
	// Deprecated flags are hidden from --help and print a notice when used.
	createCmd.Flags().StringVarP(&createApplyFile, "template", "t", "", "")
	_ = createCmd.Flags().MarkDeprecated("template", "use --apply-file/-a instead")
	createCmd.Flags().StringVarP(&createImage, "image", "i", defaultImage, "boot image to use (list them with boxctl images)")
	createCmd.Flags().StringVarP(&createSize, "size", "s", "", "box size: small, medium or large (list them with boxctl sizes; default small)")
	_ = createCmd.RegisterFlagCompletionFunc("size", completeSize)
	rootCmd.AddCommand(createCmd)
}

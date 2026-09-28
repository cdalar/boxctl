package cmd

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/cdalar/boxctl/internal/client"
)

// allBoxes is the magic <name> that makes rm/pause/resume act on every
// box the caller owns (filtered by wantState), the same way
// `onctl destroy all` works -- a box literally named "all" can't be
// targeted individually by these commands.
const allBoxes = "all"

// forEachBox runs fn concurrently against every box whose state is in
// wantState (every box, if wantState is empty), printing a ✔/✘ line per
// box, and returns an error if any of them failed.
func forEachBox(ctx context.Context, c *client.Client, verb string, wantState []string, fn func(ctx context.Context, name string) error) error {
	vms, err := c.List(ctx)
	if err != nil {
		return err
	}

	var names []string
	for _, vm := range vms {
		if len(wantState) == 0 || slices.Contains(wantState, vm.State) {
			names = append(names, vm.Name)
		}
	}
	if len(names) == 0 {
		fmt.Printf("No boxes to %s.\n", verb)
		return nil
	}

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		failed int
	)
	for _, name := range names {
		wg.Go(func() {
			err := fn(ctx, name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fmt.Printf("\033[31m✘\033[0m %s: %v\n", name, err)
				failed++
				return
			}
			fmt.Printf("\033[32m✔\033[0m %s\n", name)
		})
	}
	wg.Wait()

	if failed > 0 {
		return fmt.Errorf("failed to %s %d of %d box(es)", verb, failed, len(names))
	}
	return nil
}

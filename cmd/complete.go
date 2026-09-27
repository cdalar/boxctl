package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
)

// completionTimeout bounds the List call behind a <TAB> -- a slow or
// unreachable API should cost the user a short pause, not a hung shell.
const completionTimeout = 3 * time.Second

// completeBoxName is the ValidArgsFunction for every command whose one
// positional argument is an existing box's name: it offers the caller's
// box names (with their state as the description) for the first
// argument, and nothing -- not even local files -- after that. Any
// failure (not logged in, API unreachable) just means no suggestions.
func completeBoxName(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 || cfg == nil || cfg.Token == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
	defer cancel()
	vms, err := newClient().List(ctx)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	names := make([]cobra.Completion, 0, len(vms))
	for _, vm := range vms {
		names = append(names, cobra.CompletionWithDesc(vm.Name, vm.State))
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

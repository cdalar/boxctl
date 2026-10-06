package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// addOutputFlag gives a listing command `-o/--output`, like `onctl ls`.
func addOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("output", "o", "table", "output format (table, json)")
	_ = cmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"table", "json"}, cobra.ShellCompDirectiveNoFileComp
	})
}

// wantJSON reports whether the command was run with `-o json`. Call it
// before talking to the server, so a mistyped format fails right away.
func wantJSON(cmd *cobra.Command) (bool, error) {
	switch format, _ := cmd.Flags().GetString("output"); format {
	case "table":
		return false, nil
	case "json":
		return true, nil
	default:
		return false, fmt.Errorf("unknown output format %q (want table or json)", format)
	}
}

// printJSON writes a listing as an indented JSON array, in the fields the
// API returns it in. An empty listing is `[]`, never `null` or the table
// view's "nothing here" sentence, so a script can always parse it.
func printJSON[T any](items []T) error {
	if items == nil {
		items = []T{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

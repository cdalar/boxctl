package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// addOutputFlag gives a listing command `-o/--output`, like `onctl ls`:
// table or json, plus any of extra (ls's wide).
func addOutputFlag(cmd *cobra.Command, extra ...string) {
	formats := append([]string{"table"}, extra...)
	formats = append(formats, "json")
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations["output"] = strings.Join(formats, ",")
	cmd.Flags().StringP("output", "o", "table", "output format ("+strings.Join(formats, ", ")+")")
	_ = cmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return formats, cobra.ShellCompDirectiveNoFileComp
	})
}

// outputFormat is the command's `-o`, one of those addOutputFlag gave
// it. Call it before talking to the server, so a mistyped format fails
// right away.
func outputFormat(cmd *cobra.Command) (string, error) {
	format, _ := cmd.Flags().GetString("output")
	formats := strings.Split(cmd.Annotations["output"], ",")
	if !slices.Contains(formats, format) {
		return "", fmt.Errorf("unknown output format %q (want %s)", format, strings.Join(formats, ", "))
	}
	return format, nil
}

// wantJSON reports whether the command was run with `-o json`, as
// outputFormat checks it.
func wantJSON(cmd *cobra.Command) (bool, error) {
	format, err := outputFormat(cmd)
	return format == "json", err
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

package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var labelCmd = &cobra.Command{
	Use:               "label <name> [key=value ...] [key- ...]",
	ValidArgsFunction: completeBoxName,
	Short:             "Show or change a box's labels",
	Long: `With just a box, prints its labels. key=value sets a label and key-
removes one; the others stay as they are. A key is 1-63 characters of
a-z, 0-9, '.' and '-'. boxctl claude labels the boxes it creates
(boxctl.run, boxctl.project, ...) and finds them again by those.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		c := newClient()
		vms, err := c.List(ctx)
		if err != nil {
			return err
		}
		var labels map[string]string
		found := false
		for _, vm := range vms {
			if vm.Name == args[0] {
				labels, found = maps.Clone(vm.Labels), true
				break
			}
		}
		if !found {
			return fmt.Errorf("no box %s", args[0])
		}
		if len(args) > 1 {
			if labels, err = applyLabelArgs(labels, args[1:]); err != nil {
				return err
			}
			vm, err := c.SetLabels(ctx, args[0], labels)
			if err != nil {
				return err
			}
			labels = vm.Labels
		}
		for _, k := range slices.Sorted(maps.Keys(labels)) {
			fmt.Printf("%s=%s\n", k, labels[k])
		}
		return nil
	},
}

// applyLabelArgs applies key=value and key- arguments to labels.
func applyLabelArgs(labels map[string]string, args []string) (map[string]string, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok && k != "" {
			labels[k] = v
		} else if k, ok := strings.CutSuffix(a, "-"); ok && k != "" {
			delete(labels, k)
		} else {
			return nil, fmt.Errorf("%q: want key=value or key-", a)
		}
	}
	return labels, nil
}

func init() {
	rootCmd.AddCommand(labelCmd)
}

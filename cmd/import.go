package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var importName string

var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import a previously-downloaded snapshot bundle as a new, paused box",
	Long: `Import a bundle previously produced by 'boxctl download' as a new,
paused box -- ready for 'boxctl resume' to actually boot it, same as a
box paused on this account all along.

The bundle is uploaded directly to object storage and reconstructed by
the target host's agent from there (see boxctl-vms's
docs/plans/s3-transfer.md) -- boxctl-vms itself is never in the byte
path. Not yet supported against a boxctl-vms server running in
local/dev mode.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		name := importName
		if name == "" {
			name = defaultImportName(path)
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("opening %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}

		fmt.Printf("Importing %s as %s (this can take a while for a large bundle)...\n", path, name)
		vm, err := newClient().Import(cmd.Context(), name, f, info.Size())
		if err != nil {
			return err
		}
		fmt.Printf("Imported %s (%s) -- run `boxctl resume %s` to boot it\n", vm.Name, vm.State, vm.Name)
		return nil
	},
}

// defaultImportName derives a box name from path's basename when --name
// isn't given, stripping the extension `boxctl download`'s own default
// output name uses (<name>.tar.zst) so re-importing a file downloaded
// without -o just works without also needing --name.
func defaultImportName(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".tar.zst")
	base = strings.TrimSuffix(base, ".tar")
	return base
}

func init() {
	importCmd.Flags().StringVarP(&importName, "name", "n", "", "name for the imported box (default: derived from the file name)")
	rootCmd.AddCommand(importCmd)
}

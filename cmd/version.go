package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version and BuildTime are set at build time via -ldflags "-X
// github.com/cdalar/boxctl/cmd.Version=..." -- by both .goreleaser*.yml
// (v<tag>-<commit>, and the build's date) and the Makefile. A plain `go
// build` sets neither, so versionInfo falls back to the commit and commit
// time Go records in the binary itself.
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// versionInfo is the version, build time and Go version to print.
func versionInfo() (version, built, goVersion string) {
	version, built = Version, BuildTime
	if info, ok := debug.ReadBuildInfo(); ok {
		settings := make(map[string]string)
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if rev := settings["vcs.revision"]; version == "dev" && len(rev) >= 7 {
			version = "dev-" + rev[:7]
			if settings["vcs.modified"] == "true" {
				version += "-dirty"
			}
		}
		if t := settings["vcs.time"]; built == "unknown" && t != "" {
			built = t
		}
	}
	// The compiler's own version: always right, so never an ldflag.
	return version, built, runtime.Version()
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the boxctl version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		version, built, goVersion := versionInfo()
		fmt.Printf("boxctl %s (built %s, %s)\n", version, built, goVersion)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

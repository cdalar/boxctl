package main

import (
	"os"

	"github.com/boxctl-io/boxctl-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		cmd.PrintError(err)
		os.Exit(1)
	}
}

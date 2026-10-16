package main

import (
	"os"

	"github.com/cdalar/boxctl/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		cmd.PrintError(err)
		os.Exit(1)
	}
}

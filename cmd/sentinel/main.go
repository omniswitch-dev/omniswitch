package main

import (
	"fmt"
	"os"

	"sentinel/internal/cli"
)

func main() {
	if err := cli.NewRootCommand("sentinel").Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

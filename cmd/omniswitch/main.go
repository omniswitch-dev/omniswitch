package main

import (
	"fmt"
	"os"

	"github.com/omniswitch-dev/omniswitch/internal/cli"
)

func main() {
	if err := cli.NewRootCommand("omniswitch").Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
)

// cliAction is what main should do once command-line arguments have been
// dispatched.
type cliAction int

const (
	// actionServe starts the gateway server.
	actionServe cliAction = iota
	// actionExit means the command already produced its output; main should
	// exit with the returned status code without starting a server.
	actionExit
)

const helpText = `OmniSwitch AI Gateway

Usage:
  omniswitch [command]

Commands:
  serve             Start the gateway server (default when no command is given)
  validate-config   Validate a gateway config file and exit (defaults to $OMNISWITCH_CONFIG)
  version           Print version information
  help              Show this help text

Flags:
  -h, --help        Show this help text
  -v, --version     Print version information

Environment:
  OPENAI_API_KEY, ANTHROPIC_API_KEY, and friends configure providers.
  See docs/CONFIGURATION.md and docs/CLI.md for the full reference.
`

// dispatch interprets os.Args[1:] and returns what main should do next. It
// never starts the server or binds a port itself, so --help and --version
// are safe to run even when the configured listen address is already in
// use. stdout/stderr are injected for testability.
func dispatch(args []string, stdout, stderr io.Writer, env func(string) string) (cliAction, int) {
	if len(args) == 0 {
		return actionServe, 0
	}

	switch args[0] {
	case "serve":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "omniswitch serve: unexpected argument %q\n", args[1])
			return actionExit, 2
		}
		return actionServe, 0
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, versionString())
		return actionExit, 0
	case "help", "--help", "-h":
		fmt.Fprint(stdout, helpText)
		return actionExit, 0
	case "validate-config":
		path := ""
		if len(args) > 1 {
			path = args[1]
		} else if env != nil {
			path = env("OMNISWITCH_CONFIG")
		}
		path = strings.TrimSpace(path)
		if path == "" {
			fmt.Fprintln(stderr, "omniswitch validate-config: a config path is required (pass one, or set OMNISWITCH_CONFIG)")
			return actionExit, 2
		}
		if _, err := gatewayconfig.LoadFile(path); err != nil {
			fmt.Fprintf(stderr, "invalid config %s: %v\n", path, err)
			return actionExit, 1
		}
		fmt.Fprintf(stdout, "OK: %s\n", path)
		return actionExit, 0
	default:
		fmt.Fprintf(stderr, "omniswitch: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, helpText)
		return actionExit, 1
	}
}

func versionString() string {
	return fmt.Sprintf("omniswitch version %s (commit %s, built %s)", version, commit, date)
}

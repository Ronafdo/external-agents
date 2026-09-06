package main

import (
	"fmt"
	"io"
	"os"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/aider"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: entire-agent-aider <subcommand> [args]")
	}
	if err := aider.Run(os.Args[1], os.Args[2:], os.Stdin, os.Stdout); err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }

var _ io.Reader = os.Stdin

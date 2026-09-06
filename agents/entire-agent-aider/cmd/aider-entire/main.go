package main

import (
	"fmt"
	"os"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/aider"
)

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) > 0 && args[0] == "checkpoint":
		err = aider.Checkpoint(args[1:], os.Stdout)
	case len(args) > 0 && args[0] == "resume":
		err = aider.Resume(args[1:], os.Stdout)
	default:
		err = aider.Launch(args, os.Stdin, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "aider-entire:", err)
		os.Exit(1)
	}
}

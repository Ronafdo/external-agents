package main

import (
	"fmt"
	"os"

	"github.com/entireio/external-agents/agents/entire-agent-aider/internal/aider"
)

func main() {
	if err := aider.Launch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "aider-entire:", err)
		os.Exit(1)
	}
}

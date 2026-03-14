package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
	"github.com/mistakeknot/intermix/internal/eval"
)

func main() {
	s := server.NewMCPServer(
		"intermix",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	eval.RegisterAll(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "intermix-mcp: %v\n", err)
		os.Exit(1)
	}
}

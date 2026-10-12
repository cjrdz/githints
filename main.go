package main

import (
	"os"

	"github.com/cjrdz/githints/internal/cli"
)

// version is set by goreleaser at build time via -X main.version=...
var version string

func main() {
	os.Exit(cli.Main(os.Args, version))
}

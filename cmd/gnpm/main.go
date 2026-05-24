// Command gnpm is an npm/pnpm-compatible package manager. See doc/spec.md
// for the behavioral contract.
package main

import (
	"os"

	"github.com/koji-1009/gnpm/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}

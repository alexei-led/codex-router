// Command codex-router routes Codex CLI requests to a model and reasoning
// effort per step. See docs/research.md and docs/architecture-options.html.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "codex-router: not implemented yet")
	os.Exit(2)
}

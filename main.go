// Command yoink clones every repository in an organization and keeps the clones up to date.
package main

import (
	"os"

	"github.com/syntaqx/yoink/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}

// Command wml-update is a cross-platform updater for GitHub Releases and
// self-hosted static manifests.
package main

import (
	"os"

	"github.com/yxxbc/wmlUpdate/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
